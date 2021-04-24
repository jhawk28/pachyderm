package serviceenv

import (
	"context"

	"github.com/pachyderm/pachyderm/v2/src/client"

	etcd "github.com/coreos/etcd/clientv3"
	loki "github.com/grafana/loki/pkg/logcli/client"
	"github.com/jmoiron/sqlx"
	"golang.org/x/sync/errgroup"
	kube "k8s.io/client-go/kubernetes"
)

// TestServiceEnv is a simple implementation of ServiceEnv that can be
// constructed with existing clients.
type TestServiceEnv struct {
	Configuration *Configuration
	PachClient    *client.APIClient
	EtcdClient    *etcd.Client
	KubeClient    *kube.Clientset
	LokiClient    *loki.Client
	DBClient      *sqlx.DB
	Ctx           context.Context
}

// Config implements the corresponding ServiceEnv method for TestServiceEnv
func (s *TestServiceEnv) Config() *Configuration {
	return s.Configuration
}

// GetPachClient implements the corresponding ServiceEnv method for
// TestServiceEnv
func (s *TestServiceEnv) GetPachClient(ctx context.Context) *client.APIClient {
	return s.PachClient
}

// GetEtcdClient implements the corresponding ServiceEnv method for
// TestServiceEnv
func (s *TestServiceEnv) GetEtcdClient() *etcd.Client {
	return s.EtcdClient
}

// GetKubeClient implements the corresponding ServiceEnv method for
// TestServiceEnv
func (s *TestServiceEnv) GetKubeClient() *kube.Clientset {
	return s.KubeClient
}

// GetLokiClient implements the corresponding ServiceEnv method for
// TestServiceEnv
func (s *TestServiceEnv) GetLokiClient() (*loki.Client, error) {
	return s.LokiClient, nil
}

// GetDBClient implements the corresponding ServiceEnv method for TestServiceEnv
func (s *TestServiceEnv) GetDBClient() *sqlx.DB {
	return s.DBClient
}

// Context implements the corresponding ServiceEnv method for TestServiceEnv
func (s *TestServiceEnv) Context() context.Context {
	return s.Ctx
}

// ClusterID implements the corresponding ServiceEnv method for TestServiceEnv
func (s *TestServiceEnv) ClusterID() string {
	return "testing"
}

func (s *TestServiceEnv) Close() error {
	eg := &errgroup.Group{}
	eg.Go(s.GetPachClient(context.Background()).Close)
	eg.Go(s.GetEtcdClient().Close)
	eg.Go(s.GetDBClient().Close)
	return eg.Wait()
}
