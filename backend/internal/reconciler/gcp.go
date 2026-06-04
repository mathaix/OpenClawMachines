package reconciler

import (
	"context"
	"errors"
	"strings"

	compute "cloud.google.com/go/compute/apiv1"
	computepb "cloud.google.com/go/compute/apiv1/computepb"
	"google.golang.org/api/googleapi"
)

// GCPInstanceChecker checks GCP Compute Engine for instance existence.
type GCPInstanceChecker struct {
	client *compute.InstancesClient
}

// NewGCPInstanceChecker creates a new GCPInstanceChecker.
func NewGCPInstanceChecker(client *compute.InstancesClient) *GCPInstanceChecker {
	return &GCPInstanceChecker{client: client}
}

// InstanceExists returns true if the instance exists, false if it's gone (404),
// or an error for other API failures.
func (c *GCPInstanceChecker) InstanceExists(ctx context.Context, project, zone, vmName string) (bool, error) {
	_, err := c.client.Get(ctx, &computepb.GetInstanceRequest{
		Project:  project,
		Zone:     zone,
		Instance: vmName,
	})
	if err != nil {
		// Check for 404 using errors.As (the error may be wrapped)
		var gerr *googleapi.Error
		if errors.As(err, &gerr) && gerr.Code == 404 {
			return false, nil
		}
		// Also check the error string as a fallback — some SDK versions wrap differently
		if strings.Contains(err.Error(), "Error 404") || strings.Contains(err.Error(), "was not found") {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
