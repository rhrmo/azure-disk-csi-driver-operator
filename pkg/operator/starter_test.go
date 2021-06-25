package operator

import (
	"context"
	cfgV1 "github.com/openshift/api/config/v1"
	"github.com/openshift/client-go/config/clientset/versioned/fake"
	"github.com/stretchr/testify/assert"
	metaV1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"testing"
)

func TestAzureStackHubDetectionHappyPath(t *testing.T) {
	ctx := context.Background()
	infrastructure := mockInfra(cfgV1.AzureStackCloud)
	cfg := fake.NewSimpleClientset(infrastructure).ConfigV1()

	hub, err := runningOnAzureStackHub(ctx, cfg)
	assert.Nil(t, err)
	assert.True(t, hub)
}

func TestAzureStackHubDetectionOnAzurePublicCloud(t *testing.T) {
	ctx := context.Background()
	infrastructure := mockInfra(cfgV1.AzurePublicCloud)
	cfg := fake.NewSimpleClientset(infrastructure).ConfigV1()

	hub, err := runningOnAzureStackHub(ctx, cfg)
	assert.Nil(t, err)
	assert.False(t, hub)
}

func mockInfra(cloudName cfgV1.AzureCloudEnvironment) *cfgV1.Infrastructure {
	infrastructure := &cfgV1.Infrastructure{
		ObjectMeta: metaV1.ObjectMeta{
			Name: infraConfigName,
		},
		Status: cfgV1.InfrastructureStatus{
			PlatformStatus: &cfgV1.PlatformStatus{
				Azure: &cfgV1.AzurePlatformStatus{
					CloudName: cloudName,
				},
			},
		},
	}
	return infrastructure
}
