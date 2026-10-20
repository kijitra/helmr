package main

import (
	"errors"
	"os"
	"uuid"

	"github.com/helmrdotdev/helmr/internal/localmodel"
)

// Deliberately wired only in the development control plane, never production.
func loadLocalModel() (*localmodel.Gateway, error) {
	environment := os.Getenv("HELMR_LOCAL_MODEL_ENVIRONMENT_ID")
	endpoint := os.Getenv("HELMR_LOCAL_MODEL_ENDPOINT")
	if environment == "" && endpoint == "" {
		return nil, nil
	}
	id, err := uuid.Parse(environment)
	if err != nil || id.String() != environment || endpoint == "" {
		return nil, errors.New("local model requires an explicit development environment and loopback endpoint")
	}
	return localmodel.New(endpoint, id)
}
