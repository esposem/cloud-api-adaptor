// Copyright Confidential Containers Contributors
// SPDX-License-Identifier: Apache-2.0

package proxy

import (
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/confidential-containers/cloud-api-adaptor/pkg/util/agentproto"
	cri "github.com/containerd/containerd/pkg/cri/annotations"
	crio "github.com/containers/podman/v4/pkg/annotations"
	"github.com/gogo/protobuf/types"
	pb "github.com/kata-containers/kata-containers/src/runtime/virtcontainers/pkg/agent/protocols/grpc"
	criapi "k8s.io/cri-api/pkg/apis/runtime/v1alpha2"
)

type proxyService struct {
	agentproto.Redirector

	criClient *criClient
}

func newProxyService(dialer func(context.Context) (net.Conn, error), criClient *criClient) *proxyService {

	redirector := agentproto.NewRedirector(dialer)

	return &proxyService{
		Redirector: redirector,
		criClient:  criClient,
	}
}

func (s *proxyService) getImageFromDigest(ctx context.Context, digest string) (string, error) {
	if s.criClient == nil {
		return "", fmt.Errorf("getImageFromDigest: criClient is nil.")
	}

	req := &criapi.ListImagesRequest{}
	resp, err := s.criClient.ImageServiceClient.ListImages(ctx, req)
	if err != nil {
		return "", err
	}

	images := resp.GetImages()
	for _, img := range images {
		logger.Printf("imageTag: %s, image digest: %s", img.RepoTags[0], img.Id)
		if img.Id == digest {
			return img.RepoTags[0], nil
		}
	}
	return "", fmt.Errorf("Did not find imageTag from image digest %s", digest)
}

// TODO: parameterize the pause container image name
const pauseContainerImage = "k8s.gcr.io/pause:3.7"

func getImageName(annotations map[string]string) (string, error) {

	for _, a := range []string{cri.ImageName, crio.ImageName} {
		if image, ok := annotations[a]; ok {
			return image, nil
		}
	}

	for containerType, containerTypeSandbox := range map[string]string{
		cri.ContainerType:  cri.ContainerTypeSandbox,
		crio.ContainerType: crio.ContainerTypeSandbox,
	} {
		if annotations[containerType] == containerTypeSandbox {
			return pauseContainerImage, nil
		}
	}

	return "", fmt.Errorf("container image name is not specified in annotations: %#v", annotations)
}

// AgentServiceService methods

func (s *proxyService) CreateContainer(ctx context.Context, req *pb.CreateContainerRequest) (*types.Empty, error) {

	logger.Printf("CreateContainer: containerID:%s", req.ContainerId)
	if len(req.OCI.Annotations) > 0 {
		logger.Print("    annotations:")
		for k, v := range req.OCI.Annotations {
			logger.Printf("        %s: %s", k, v)
		}
	}
	if len(req.OCI.Mounts) > 0 {
		logger.Print("    mounts:")
		for _, m := range req.OCI.Mounts {
			logger.Printf("        destination:%s source:%s type:%s", m.Destination, m.Source, m.Type)
		}
	}
	if len(req.Storages) > 0 {
		logger.Print("    storages:")
		for _, s := range req.Storages {
			logger.Printf("        mount_point:%s source:%s fstype:%s driver:%s", s.MountPoint, s.Source, s.Fstype, s.Driver)
		}
	}
	if len(req.Devices) > 0 {
		logger.Print("    devices:")
		for _, d := range req.Devices {
			logger.Printf("        container_path:%s vm_path:%s type:%s", d.ContainerPath, d.VmPath, d.Type)
		}
	}
	imageName, err := getImageName(req.OCI.Annotations)
	if err != nil {
		logger.Printf("CreateContainer: image name is not available in CreateContainerRequest: %v", err)
	} else {
		// Get the imageName from digest
		if strings.HasPrefix(imageName, "sha256:") {
			digest := imageName
			logger.Printf("CreateContainer: get imageName from digest %q", digest)
			imageName, err = s.getImageFromDigest(ctx, digest)
			if err != nil {
				return nil, err
			}
		}

		logger.Printf("CreateContainer: calling PullImage for %q before CreateContainer", imageName)

		pullImageReq := &pb.PullImageRequest{
			Image:       imageName,
			ContainerId: req.ContainerId,
		}

		pullImageRes, pullImageErr := s.Redirector.PullImage(ctx, pullImageReq)

		if pullImageErr != nil {
			logger.Printf("CreateContainer: failed to call PullImage, probably because the image has already been pulled. ignored: %v", pullImageErr)
		} else {
			logger.Printf("CreateContainer: successfully pulled image %q", pullImageRes.ImageRef)
		}
	}

	res, err := s.Redirector.CreateContainer(ctx, req)

	if err != nil {
		logger.Printf("CreateContainer fails: %v", err)
	}

	return res, err
}

func (s *proxyService) StartContainer(ctx context.Context, req *pb.StartContainerRequest) (*types.Empty, error) {

	logger.Printf("StartContainer: containerID:%s", req.ContainerId)

	res, err := s.Redirector.StartContainer(ctx, req)

	if err != nil {
		logger.Printf("StartContainer fails: %v", err)
	}

	return res, err
}

func (s *proxyService) RemoveContainer(ctx context.Context, req *pb.RemoveContainerRequest) (*types.Empty, error) {

	logger.Printf("RemoveContainer: containerID:%s", req.ContainerId)

	res, err := s.Redirector.RemoveContainer(ctx, req)

	if err != nil {
		logger.Printf("RemoveContainer fails: %v", err)
	}

	return res, err
}

func (s *proxyService) CreateSandbox(ctx context.Context, req *pb.CreateSandboxRequest) (*types.Empty, error) {

	logger.Printf("CreateSandbox: hostname:%s sandboxId:%s", req.Hostname, req.SandboxId)
	if len(req.Dns) > 0 {
		logger.Print("    dns:")
		for _, d := range req.Dns {
			logger.Printf("        %s", d)
		}

		logger.Print("      Eliminated the DNS setting above from CreateSandboxRequest to stop updating /etc/resolv.conf on the peer pod VM")
		logger.Print("      See https://github.com/confidential-containers/cloud-api-adaptor/issues/98 for the details.")
		logger.Println()
		req.Dns = nil
	}
	if len(req.Storages) > 0 {
		logger.Print("    storages:")
		for _, s := range req.Storages {
			logger.Printf("        mountpoint:%s source:%s fstype:%s driver:%s", s.MountPoint, s.Source, s.Fstype, s.Driver)
		}
	}

	res, err := s.Redirector.CreateSandbox(ctx, req)

	if err != nil {
		logger.Printf("CreateSandbox fails: %v", err)
	}

	return res, err
}

func (s *proxyService) DestroySandbox(ctx context.Context, req *pb.DestroySandboxRequest) (*types.Empty, error) {

	logger.Printf("DestroySandbox")

	res, err := s.Redirector.DestroySandbox(ctx, req)

	if err != nil {
		logger.Printf("DestroySandbox fails: %v", err)
	}

	return res, err
}

func (s *proxyService) PullImage(ctx context.Context, req *pb.PullImageRequest) (*pb.PullImageResponse, error) {

	logger.Printf("PullImage: image:%s containerID:%s", req.Image, req.ContainerId)

	res, err := s.Redirector.PullImage(ctx, req)

	if err != nil {
		logger.Printf("PullImage fails: %v", err)
	}

	return res, err
}