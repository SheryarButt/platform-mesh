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

package subroutines

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/hashicorp/go-multierror"

	pmuiv1alpha1 "go.platform-mesh.io/apis/ui/v1alpha1"
	"go.platform-mesh.io/extension-manager-operator/pkg/transformer"
	"go.platform-mesh.io/extension-manager-operator/pkg/validation"
	"go.platform-mesh.io/golang-commons/logger"
	"go.platform-mesh.io/subroutines"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	ContentConfigurationSubroutineName = "ContentConfigurationSubroutine"
	ValidationConditionType            = "Valid"
	ValidationConditionReasonSuccess   = "ValidationSucceeded"
	ValidationConditionReasonFailed    = "ValidationFailed"
	ConditionStatusTrue                = "True"
	ConditionStatusFalse               = "False"
	entityTypeFinalizerName            = "extension-manager.platform-mesh.io/entity-type-registry"
)

var _ subroutines.Processor = (*ContentConfigurationSubroutine)(nil)
var _ subroutines.Finalizer = (*ContentConfigurationSubroutine)(nil)

type ContentConfigurationSubroutine struct {
	httpClient       *http.Client
	validator        validation.ExtensionConfiguration
	transformer      []transformer.ContentConfigurationTransformer
	k8sReader        ctrlruntimeclient.Reader
	entityRegistry   *validation.EntityTypeRegistry
	registryInitMu   sync.Mutex
	registryInitDone atomic.Bool
}

func NewContentConfigurationSubroutine(validator validation.ExtensionConfiguration,
	httpClient *http.Client, k8sReader ctrlruntimeclient.Reader, registry *validation.EntityTypeRegistry) (*ContentConfigurationSubroutine, error) {
	if registry != nil && k8sReader == nil {
		return nil, fmt.Errorf("entity type registry requires a k8s reader")
	}
	transformers := []transformer.ContentConfigurationTransformer{
		&transformer.UrlSuffixTransformer{},
	}
	return &ContentConfigurationSubroutine{
		httpClient:     httpClient,
		validator:      validator,
		transformer:    transformers,
		k8sReader:      k8sReader,
		entityRegistry: registry,
	}, nil
}

func (r *ContentConfigurationSubroutine) GetName() string {
	return ContentConfigurationSubroutineName
}

func (r *ContentConfigurationSubroutine) Process(ctx context.Context, obj ctrlruntimeclient.Object) (subroutines.Result, error) {
	log := logger.LoadLoggerFromContext(ctx)

	instance, ok := obj.(*pmuiv1alpha1.ContentConfiguration)
	if !ok {
		return subroutines.OK(), fmt.Errorf("expected *v1alpha1.ContentConfiguration, got %T", obj)
	}

	log.Debug().Str("name", instance.Name).Msg("processing content configuration")

	// Initialize entity type registry on first reconcile
	if r.entityRegistry != nil && !r.registryInitDone.Load() {
		r.registryInitMu.Lock()
		if !r.registryInitDone.Load() {
			if initErr := r.initEntityTypeRegistry(ctx, log); initErr != nil {
				r.registryInitMu.Unlock()
				return subroutines.OK(), initErr
			}
			r.registryInitDone.Store(true)
		}
		r.registryInitMu.Unlock()
	}

	// Download or Retrieve ContentConfiguration Json
	contentType, rawConfig, err := r.retrieveContentConfigurationData(instance, log)
	if err != nil {
		return subroutines.OK(), err
	}

	// Validate ContentConfiguration Json
	validatedConfig, valErr := r.validator.Validate(rawConfig, contentType)
	if valErr != nil && valErr.Len() > 0 {
		log.Err(valErr).Msg("failed to validate configuration")
		condition := metav1.Condition{
			Type:    ValidationConditionType,
			Status:  ConditionStatusFalse,
			Reason:  ValidationConditionReasonFailed,
			Message: valErr.Error(),
		}
		meta.SetStatusCondition(&instance.Status.Conditions, condition)
		return subroutines.OK(), nil
	}

	// Parse the validated JSON into a ContentConfiguration struct
	contentConfiguration := &validation.ContentConfiguration{}
	err = json.Unmarshal([]byte(validatedConfig), contentConfiguration)
	if err != nil {
		return subroutines.OK(), fmt.Errorf("failed to unmarshal contentConfiguration: %w", err)
	}

	// Validate entityType references against the parsed struct directly
	if r.entityRegistry != nil {
		entityTypeErrs := r.entityRegistry.Validate(*contentConfiguration)
		if len(entityTypeErrs) > 0 {
			merr := &multierror.Error{}
			for _, e := range entityTypeErrs {
				merr = multierror.Append(merr, e)
			}
			log.Err(merr).Msg("failed to validate entity types")
			condition := metav1.Condition{
				Type:    ValidationConditionType,
				Status:  ConditionStatusFalse,
				Reason:  ValidationConditionReasonFailed,
				Message: merr.Error(),
			}
			meta.SetStatusCondition(&instance.Status.Conditions, condition)
			return subroutines.OK(), nil
		}
	}

	condition := metav1.Condition{
		Type:    ValidationConditionType,
		Status:  ConditionStatusTrue,
		Reason:  ValidationConditionReasonSuccess,
		Message: "OK",
	}
	meta.SetStatusCondition(&instance.Status.Conditions, condition)

	// Transform ContentConfiguration
	for _, configurationTransformer := range r.transformer {
		err := configurationTransformer.Transform(contentConfiguration, instance)
		if err != nil {
			return subroutines.OK(), fmt.Errorf("failed to transform contentConfiguration: %w", err)
		}
	}

	validatedConfigBytes, err := json.Marshal(contentConfiguration)
	if err != nil {
		return subroutines.OK(), fmt.Errorf("failed to marshal contentConfiguration: %w", err)
	}
	validatedConfig = string(validatedConfigBytes)

	// Store resulting configuration in the status
	instance.Status.ConfigurationResult = validatedConfig

	// Update entity type registry with this CC's definitions
	if r.entityRegistry != nil {
		r.entityRegistry.LoadForOwner(ownerKey(instance), *contentConfiguration)
	}

	return subroutines.OK(), nil
}

func (r *ContentConfigurationSubroutine) Finalizers(_ ctrlruntimeclient.Object) []string {
	if r.entityRegistry == nil {
		return nil
	}
	return []string{entityTypeFinalizerName}
}

func (r *ContentConfigurationSubroutine) Finalize(_ context.Context, obj ctrlruntimeclient.Object) (subroutines.Result, error) {
	if r.entityRegistry == nil {
		return subroutines.OK(), nil
	}
	instance, ok := obj.(*pmuiv1alpha1.ContentConfiguration)
	if !ok {
		return subroutines.OK(), fmt.Errorf("expected *v1alpha1.ContentConfiguration, got %T", obj)
	}
	r.entityRegistry.RemoveOwner(ownerKey(instance))
	return subroutines.OK(), nil
}

func ownerKey(cc *pmuiv1alpha1.ContentConfiguration) string {
	if cc.UID != "" {
		return string(cc.UID)
	}
	return cc.Namespace + "/" + cc.Name
}

func (r *ContentConfigurationSubroutine) initEntityTypeRegistry(ctx context.Context, log *logger.Logger) error {
	var ccList pmuiv1alpha1.ContentConfigurationList
	if err := r.k8sReader.List(ctx, &ccList); err != nil {
		log.Warn().Err(err).Msg("failed to list ContentConfigurations for entity type registry initialization, registry will be populated incrementally")
		return nil
	}

	configs := make(map[string]validation.ContentConfiguration)
	for i := range ccList.Items {
		cc := &ccList.Items[i]
		if cc.Status.ConfigurationResult == "" {
			continue
		}
		var parsed validation.ContentConfiguration
		if err := json.Unmarshal([]byte(cc.Status.ConfigurationResult), &parsed); err != nil {
			log.Warn().Err(err).Str("name", cc.Name).Msg("failed to parse ConfigurationResult for entity type registry")
			continue
		}
		configs[ownerKey(cc)] = parsed
	}

	r.entityRegistry.BulkloadWithOwners(configs)
	log.Info().Int("entityTypes", len(r.entityRegistry.KnownTypes())).Msg("initialized entity type registry")
	return nil
}

func (r *ContentConfigurationSubroutine) retrieveContentConfigurationData(instance *pmuiv1alpha1.ContentConfiguration, log *logger.Logger) (string, []byte, error) {
	var contentType string
	var rawConfig []byte
	// InlineConfiguration has higher priority than RemoteConfiguration
	switch {
	case instance.Spec.InlineConfiguration != nil && instance.Spec.InlineConfiguration.Content != "":
		contentType = instance.Spec.InlineConfiguration.ContentType
		rawConfig = []byte(instance.Spec.InlineConfiguration.Content)
	case instance.Spec.RemoteConfiguration != nil && instance.Spec.RemoteConfiguration.URL != "":
		url := instance.Spec.RemoteConfiguration.URL
		if instance.Spec.RemoteConfiguration.InternalUrl != "" {
			url = instance.Spec.RemoteConfiguration.InternalUrl
		}
		bytes, err := r.getRemoteConfig(url, log)
		if err != nil {
			log.Err(err).Msg("failed to fetch remote configuration")
			return "", nil, err
		}
		log.Info().Msg("fetched remote configuration")
		contentType = instance.Spec.RemoteConfiguration.ContentType
		rawConfig = bytes
	default:
		return "", nil, fmt.Errorf("no configuration provided")
	}
	return contentType, rawConfig, nil
}

// Do makes an HTTP request to the specified URL.
func (r *ContentConfigurationSubroutine) getRemoteConfig(url string, log *logger.Logger) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %v", err)
	}

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %v", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			log.Err(closeErr).Msg("failed to close response body")
		}
	}()

	if resp.StatusCode != http.StatusOK {
		// Give the caller signal to retry if we have 5xx status codes
		if resp.StatusCode >= http.StatusInternalServerError && resp.StatusCode < 600 {
			return nil, fmt.Errorf("received non-200 status code: %d", resp.StatusCode)
		}

		return nil, fmt.Errorf("received non-200 status code: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %v", err)
	}
	// TODO
	// we need to check the size of the received body before loading it to memory.
	// In case it exceeds a certain size we should reject it.
	// https://go.platform-mesh.io/extension-manager-operator/pull/23#discussion_r1622598363

	return body, nil
}
