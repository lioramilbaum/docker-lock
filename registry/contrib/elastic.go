package contrib

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/michaelperel/docker-lock/registry"
)

// ElasticWrapper is a registry wrapper for the Elasticsearch repository.
type ElasticWrapper struct {
	Client *registry.HTTPClient
}

// elasticTokenResponse contains the bearer token required to
// query the container registry for a digest.
type elasticTokenResponse struct {
	Token string `json:"token"`
}

// NewElasticWrapper creates an ElasticWrapper.
func NewElasticWrapper(client *registry.HTTPClient) *ElasticWrapper {
	w := &ElasticWrapper{}

	if client == nil {
		w.Client = &registry.HTTPClient{
			Client:        &http.Client{},
			BaseDigestURL: fmt.Sprintf("https://%sv2", w.Prefix()),
			BaseTokenURL:  "https://docker-auth.elastic.co/auth",
		}
	}

	return w
}

// Digest queries the container registry for the digest given a name and tag.
func (w *ElasticWrapper) Digest(name string, tag string) (string, error) {
	name = strings.Replace(name, w.Prefix(), "", 1)

	t, err := w.token(name)
	if err != nil {
		return "", err
	}

	url := fmt.Sprintf("%s/%s/manifests/%s", w.Client.BaseDigestURL, name, tag)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}

	req.Header.Add("Authorization", fmt.Sprintf("Bearer %s", t))
	req.Header.Add(
		"Accept", "application/vnd.docker.distribution.manifest.v2+json",
	)

	client := &http.Client{}

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	digest := resp.Header.Get("Docker-Content-Digest")
	if digest == "" {
		return "", fmt.Errorf("no digest found for '%s:%s'", name, tag)
	}

	return strings.TrimPrefix(digest, "sha256:"), nil
}

// token queries the container registry for a bearer token that is later
// required to query the container registry for a digest.
func (w *ElasticWrapper) token(name string) (string, error) {
	// example name -> "elasticsearch/elasticsearch-oss"
	url := fmt.Sprintf(
		"%s?scope=repository:%s:pull&service=token-service",
		w.Client.BaseTokenURL, name,
	)

	resp, err := http.Get(url) // nolint: gosec
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	d := json.NewDecoder(resp.Body)

	t := elasticTokenResponse{}
	if err = d.Decode(&t); err != nil {
		return "", err
	}

	return t.Token, nil
}

// Prefix returns the registry prefix that identifies the Elasticsearch
// registry.
func (w *ElasticWrapper) Prefix() string {
	return "docker.elastic.co/"
}
