package grpc

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"

	"github.com/rancher/norman/httperror"
	"github.com/rancher/rancher/pkg/auth/util"
	"github.com/rancher/rancher/pkg/clustermanager"
	v1 "github.com/rancher/types/apis/core/v1"
	"github.com/rancher/types/config"
	"github.com/rancher/types/config/dialer"
	"github.com/vgough/grpc-proxy/proxy"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

const (
	clusterIDHeaderKey = "cluster-id"
	AuthHeaderName     = "Authorization"
	AuthValuePrefix    = "Bearer"
)

type Handler struct {
	server  *grpc.Server
	manager *clustermanager.Manager
	dialer  dialer.Factory
	next    http.Handler

	secretLister v1.SecretLister
}

func Start(ctx context.Context, sctx *config.ScaledContext, manager *clustermanager.Manager) {
	lis, err := net.Listen("tcp", fmt.Sprintf(":50051"))
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}
	handler := &Handler{
		dialer:       sctx.Dialer,
		manager:      manager,
		secretLister: sctx.Core.Secrets("").Controller().Lister(),
	}
	grpcS := grpc.NewServer(
		grpc.CustomCodec(proxy.Codec()),
		grpc.UnknownServiceHandler(proxy.TransparentHandler(handler)))
	go grpcS.Serve(lis)
	go func() {
		<-ctx.Done()
		grpcS.Stop()
	}()
}

func NewMultiplexHandler(sctx *config.ScaledContext, manager *clustermanager.Manager, next http.Handler) *Handler {
	handler := &Handler{
		next:         next,
		dialer:       sctx.Dialer,
		manager:      manager,
		secretLister: sctx.Core.Secrets("").Controller().Lister(),
	}
	handler.server = grpc.NewServer(
		grpc.CustomCodec(proxy.Codec()),
		grpc.UnknownServiceHandler(proxy.TransparentHandler(handler)))
	return handler
}

func (h *Handler) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	h2c.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor == 2 && strings.Contains(r.Header.Get("Content-Type"), "application/grpc") {
			if err := h.auth(r.Header.Get(AuthHeaderName)); err != nil {
				util.ReturnHTTPError(rw, r, 401, err.Error())
				return
			}
			h.server.ServeHTTP(w, r)
		} else {
			h.next.ServeHTTP(w, r)
		}
	}), &http2.Server{}).ServeHTTP(rw, r)
}

func (h *Handler) auth(authHeader string) error {
	var tokenAuthValue string
	authHeader = strings.TrimSpace(authHeader)

	if authHeader != "" {
		parts := strings.SplitN(authHeader, " ", 2)
		if strings.EqualFold(parts[0], AuthValuePrefix) {
			if len(parts) > 1 {
				tokenAuthValue = strings.TrimSpace(parts[1])
			}
		}
	}

	if tokenAuthValue == "" {
		// no auth header, cannot authenticate
		return httperror.NewAPIErrorLong(http.StatusUnauthorized, util.GetHTTPErrorCode(http.StatusUnauthorized), "No valid token auth header")
	}
	secret, err := h.secretLister.Get("cattle-global-data", "global-monitoring")
	if err != nil {
		return err
	}
	token := string(secret.Data["token"])
	if token != tokenAuthValue {
		return httperror.NewAPIErrorLong(http.StatusUnauthorized, util.GetHTTPErrorCode(http.StatusUnauthorized), "No valid token")
	}

	return nil
}

func (h *Handler) Connect(ctx context.Context, fullMethodName string) (context.Context, *grpc.ClientConn, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ctx, nil, errors.New("Unable to get gRPC metadata from context")
	}

	//auth
	authHeader := md.Get(AuthHeaderName)
	if len(authHeader) == 0 {
		return ctx, nil, errors.New("no valid token auth header")
	}
	if err := h.auth(authHeader[0]); err != nil {
		return ctx, nil, err
	}

	MdclusterID := md.Get(clusterIDHeaderKey)
	if len(MdclusterID) == 0 {
		return ctx, nil, errors.New("empty cluster-id")
	}
	clusterID := strings.Split(MdclusterID[0], ":")[0]
	uctx, err := h.manager.UserContext(clusterID)
	if err != nil {
		return ctx, nil, err
	}
	dialer, err := h.dialer.ClusterDialer(clusterID)
	if err != nil {
		return ctx, nil, err
	}

	service, err := uctx.Core.Services("").Controller().Lister().Get("cattle-prometheus", "access-thanos")
	if err != nil {
		return ctx, nil, err
	}
	target := service.Spec.ClusterIP
	cc, err := grpc.DialContext(ctx, target+":10901",
		grpc.WithCodec(proxy.Codec()),
		grpc.WithInsecure(),
		grpc.WithContextDialer(func(i context.Context, address string) (net.Conn, error) {
			return dialer("tcp", address)
		}))
	return ctx, cc, err
}

func (h *Handler) Release(ctx context.Context, conn *grpc.ClientConn) {
	conn.Close()
}
