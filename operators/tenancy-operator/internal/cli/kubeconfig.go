/*
Copyright The Platform Mesh Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cli

import (
	"fmt"
	"os"

	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/yaml"
)

// KubeconfigOptions describes the kubeconfig to write.
type KubeconfigOptions struct {
	// Server is the kcp front-proxy base URL. The cluster segment is appended,
	// because a kubeconfig addresses ONE workspace.
	Server string

	// Cluster is the workspace to point at — a logical cluster ID or a path.
	//
	// There is no default: a client that has not chosen one reads its memberships
	// and picks. A default here would funnel every request into one workspace.
	Cluster string

	// CAFile / CAData verify the front-proxy's certificate.
	CAData []byte

	// ExecCommand is this binary's path, used as a credential plugin so tokens
	// refresh transparently. When empty, the current id_token is embedded instead —
	// simpler, but it stops working when the token expires.
	ExecCommand string
	ExecArgs    []string

	// Token is embedded when ExecCommand is empty.
	Token string

	ContextName string
}

// Kubeconfig renders a kubeconfig for one workspace.
//
// Two shapes. The exec form is preferred: the credential plugin refreshes
// without the user noticing, and there is no `--oidc-client-secret` anywhere,
// because PKCE refresh needs only the issuer and client ID.
func Kubeconfig(opts KubeconfigOptions) ([]byte, error) {
	if opts.Server == "" {
		return nil, fmt.Errorf("a server URL is required")
	}
	if opts.Cluster == "" {
		return nil, fmt.Errorf("a workspace is required: pick one from your memberships (there is no default workspace by design)")
	}

	name := opts.ContextName
	if name == "" {
		name = "tenancy"
	}

	cfg := clientcmdapi.NewConfig()
	cfg.Clusters[name] = &clientcmdapi.Cluster{
		Server:                   fmt.Sprintf("%s/clusters/%s", BaseURL(opts.Server), opts.Cluster),
		CertificateAuthorityData: opts.CAData,
	}

	authInfo := clientcmdapi.NewAuthInfo()
	switch {
	case opts.ExecCommand != "":
		authInfo.Exec = &clientcmdapi.ExecConfig{
			APIVersion:      "client.authentication.k8s.io/v1beta1",
			Command:         opts.ExecCommand,
			Args:            opts.ExecArgs,
			InteractiveMode: clientcmdapi.IfAvailableExecInteractiveMode,
		}
	case opts.Token != "":
		authInfo.Token = opts.Token
	default:
		return nil, fmt.Errorf("neither an exec command nor a token was provided")
	}
	cfg.AuthInfos[name] = authInfo

	cfg.Contexts[name] = &clientcmdapi.Context{Cluster: name, AuthInfo: name}
	cfg.CurrentContext = name

	// Marshal through the versioned type so the output is a normal kubeconfig
	// rather than the internal representation.
	data, err := yaml.Marshal(convert(cfg))
	if err != nil {
		return nil, fmt.Errorf("rendering the kubeconfig: %w", err)
	}
	return data, nil
}

// WriteKubeconfig writes to path, or stdout when path is "-".
func WriteKubeconfig(path string, data []byte) error {
	if path == "-" || path == "" {
		_, err := os.Stdout.Write(data)
		return err
	}
	// 0600: it names a credential source, and with an embedded token it IS one.
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("writing the kubeconfig to %s: %w", path, err)
	}
	return nil
}

// convert turns the internal config into the serializable v1 shape.
func convert(in *clientcmdapi.Config) map[string]any {
	clusters := make([]map[string]any, 0, len(in.Clusters))
	for name, c := range in.Clusters {
		cluster := map[string]any{"server": c.Server}
		if len(c.CertificateAuthorityData) > 0 {
			cluster["certificate-authority-data"] = c.CertificateAuthorityData
		}
		clusters = append(clusters, map[string]any{"name": name, "cluster": cluster})
	}

	users := make([]map[string]any, 0, len(in.AuthInfos))
	for name, a := range in.AuthInfos {
		user := map[string]any{}
		if a.Exec != nil {
			user["exec"] = map[string]any{
				"apiVersion":      a.Exec.APIVersion,
				"command":         a.Exec.Command,
				"args":            a.Exec.Args,
				"interactiveMode": string(a.Exec.InteractiveMode),
			}
		}
		if a.Token != "" {
			user["token"] = a.Token
		}
		users = append(users, map[string]any{"name": name, "user": user})
	}

	contexts := make([]map[string]any, 0, len(in.Contexts))
	for name, c := range in.Contexts {
		contexts = append(contexts, map[string]any{
			"name":    name,
			"context": map[string]any{"cluster": c.Cluster, "user": c.AuthInfo},
		})
	}

	return map[string]any{
		"apiVersion":      "v1",
		"kind":            "Config",
		"clusters":        clusters,
		"users":           users,
		"contexts":        contexts,
		"current-context": in.CurrentContext,
	}
}
