package secrets

import (
	"context"
	"fmt"

	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	secretmanagerpb "cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
)

// FetchSecret retrieves a secret value from GCP Secret Manager.
// resourceName should be in the format: projects/PROJECT/secrets/NAME/versions/latest
func FetchSecret(ctx context.Context, resourceName string) (string, error) {
	client, err := secretmanager.NewClient(ctx)
	if err != nil {
		return "", fmt.Errorf("secrets: create client: %w", err)
	}
	defer func() { _ = client.Close() }()

	result, err := client.AccessSecretVersion(ctx, &secretmanagerpb.AccessSecretVersionRequest{
		Name: resourceName,
	})
	if err != nil {
		return "", fmt.Errorf("secrets: access secret %s: %w", resourceName, err)
	}

	return string(result.Payload.Data), nil
}
