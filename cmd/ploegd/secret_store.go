package main

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/secretstore"
	"github.com/ploeg-hq/ploeg/pkg/secretstore/kubernetes"
	"github.com/ploeg-hq/ploeg/pkg/secretstore/openbao"
)

type secretStoreEnv struct {
	kind, address, authMount, role, kvMount, pathPrefix, namespace, caFile, kubernetesNamespace string
	allowInsecureHTTP                                                                           bool
}

func secretStoreEnvFromOS() secretStoreEnv {
	return secretStoreEnv{
		kind:                strings.TrimSpace(os.Getenv("PLOEG_SECRET_STORE")),
		address:             os.Getenv("PLOEG_SECRET_STORE_OPENBAO_ADDR"),
		authMount:           os.Getenv("PLOEG_SECRET_STORE_OPENBAO_AUTH_MOUNT"),
		role:                os.Getenv("PLOEG_SECRET_STORE_OPENBAO_ROLE"),
		kvMount:             os.Getenv("PLOEG_SECRET_STORE_OPENBAO_KV_MOUNT"),
		pathPrefix:          os.Getenv("PLOEG_SECRET_STORE_OPENBAO_PATH_PREFIX"),
		namespace:           os.Getenv("PLOEG_SECRET_STORE_OPENBAO_NAMESPACE"),
		caFile:              os.Getenv("PLOEG_SECRET_STORE_OPENBAO_CA_FILE"),
		kubernetesNamespace: os.Getenv("PLOEG_SECRET_STORE_KUBERNETES_NAMESPACE"),
		allowInsecureHTTP:   strings.TrimSpace(os.Getenv("PLOEG_SECRET_STORE_OPENBAO_ALLOW_INSECURE_HTTP")) == "true",
	}
}

func secretStore(log *slog.Logger, env secretStoreEnv) (secretstore.Store, error) {
	switch env.kind {
	case "":
		log.Info("no connection secret store configured (PLOEG_SECRET_STORE unset); store-backed connection calls fail closed")
		return secretstore.Unconfigured{}, nil
	case "openbao":
		s, err := openbao.New(openbao.Config{Address: env.address, AuthMount: env.authMount, Role: env.role,
			KVMount: env.kvMount, PathPrefix: env.pathPrefix, Namespace: env.namespace, CAFile: env.caFile, AllowInsecureHTTP: env.allowInsecureHTTP})
		if err != nil {
			return nil, fmt.Errorf("PLOEG_SECRET_STORE=openbao: %w", err)
		}
		log.Info("connection secret store configured", "store", s.Name())
		return s, nil
	case "kubernetes":
		s, err := kubernetes.New(kubernetes.Config{Namespace: env.kubernetesNamespace})
		if err != nil {
			return nil, fmt.Errorf("PLOEG_SECRET_STORE=kubernetes: %w", err)
		}
		log.Info("connection secret store configured", "store", s.Name())
		return s, nil
	}
	return nil, fmt.Errorf("PLOEG_SECRET_STORE must be empty, openbao or kubernetes, not %q", env.kind)
}
