package kubewarden

import (
	"io"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/yaml"
)

type Settings struct {
	PoliciesDownloadDir string                `json:"policiesDownloadDir"`
	Policies            map[string]PolicySpec `json:"policies"`
}

type PolicyMode string

const (
	PolicyModeProtect PolicyMode = "protect"
	PolicyModeMonitor PolicyMode = "monitor"
)

// ClusterAdmissionPolicySpec defines the desired state of ClusterAdmissionPolicy
type PolicySpec struct {
	// Module is the location of the WASM module to be loaded. Can be a
	// local file (file://), a remote file served by an HTTP server
	// (http://, https://), or an artifact served by an OCI-compatible
	// registry (registry://).
	Module string `json:"module,omitempty"`

	// Mode defines the execution mode of this policy. Can be set to
	// either "protect" or "monitor". If it's empty, it is defaulted to
	// "protect".
	// Transitioning this setting from "monitor" to "protect" is
	// allowed, but is disallowed to transition from "protect" to
	// "monitor". To perform this transition, the policy should be
	// recreated in "monitor" mode instead.
	// +kubebuilder:default:=protect
	// +optional
	Mode *PolicyMode `json:"mode,omitempty"`

	// Settings is a free-form object that contains the policy settingsuration
	Settings runtime.RawExtension `json:"settings,omitempty"`

	// Rules describes what operations on what resources/subresources the webhook cares about.
	// The webhook cares about an operation if it matches _any_ Rule.
	Rules []admissionregistrationv1.RuleWithOperations `json:"rules"`

	// FailurePolicy defines how unrecognized errors and timeout errors from the
	// policy are handled. Allowed values are "Ignore" or "Fail".
	// * "Ignore" means that an error calling the webhook is ignored and the API
	//   request is allowed to continue.
	// * "Fail" means that an error calling the webhook causes the admission to
	//   fail and the API request to be rejected.
	// The default behaviour is "Fail"
	FailurePolicy *admissionregistrationv1.FailurePolicyType `json:"failurePolicy,omitempty"`

	// Mutating indicates whether a policy has the ability to mutate
	// incoming requests or not.
	Mutating bool `json:"mutating"`

	// matchPolicy defines how the "rules" list is used to match incoming requests.
	// Allowed values are "Exact" or "Equivalent".
	//
	// - Exact: match a request only if it exactly matches a specified rule.
	// For example, if deployments can be modified via apps/v1, apps/v1beta1, and extensions/v1beta1,
	// but "rules" only included `apiGroups:["apps"], apiVersions:["v1"], resources: ["deployments"]`,
	// a request to apps/v1beta1 or extensions/v1beta1 would not be sent to the webhook.
	//
	// - Equivalent: match a request if modifies a resource listed in rules, even via another API group or version.
	// For example, if deployments can be modified via apps/v1, apps/v1beta1, and extensions/v1beta1,
	// and "rules" only included `apiGroups:["apps"], apiVersions:["v1"], resources: ["deployments"]`,
	// a request to apps/v1beta1 or extensions/v1beta1 would be converted to apps/v1 and sent to the webhook.
	//
	// Defaults to "Equivalent"
	MatchPolicy *admissionregistrationv1.MatchPolicyType `json:"matchPolicy,omitempty"`

	// NamespaceSelector decides whether to run the webhook on an object based
	// on whether the namespace for that object matches the selector. If the
	// object itself is a namespace, the matching is performed on
	// object.metadata.labels. If the object is another cluster scoped resource,
	// it never skips the webhook.
	//
	// For example, to run the webhook on any objects whose namespace is not
	// associated with "runlevel" of "0" or "1";  you will set the selector as
	// follows:
	// "namespaceSelector": {
	//   "matchExpressions": [
	//     {
	//       "key": "runlevel",
	//       "operator": "NotIn",
	//       "values": [
	//         "0",
	//         "1"
	//       ]
	//     }
	//   ]
	// }
	//
	// If instead you want to only run the webhook on any objects whose
	// namespace is associated with the "environment" of "prod" or "staging";
	// you will set the selector as follows:
	// "namespaceSelector": {
	//   "matchExpressions": [
	//     {
	//       "key": "environment",
	//       "operator": "In",
	//       "values": [
	//         "prod",
	//         "staging"
	//       ]
	//     }
	//   ]
	// }
	//
	// See
	// https://kubernetes.io/docs/concepts/overview/working-with-objects/labels
	// for more examples of label selectors.
	//
	// Default to the empty LabelSelector, which matches everything.
	NamespaceSelector *metav1.LabelSelector `json:"namespaceSelector,omitempty"`

	// ObjectSelector decides whether to run the webhook based on if the
	// object has matching labels. objectSelector is evaluated against both
	// the oldObject and newObject that would be sent to the webhook, and
	// is considered to match if either object matches the selector. A null
	// object (oldObject in the case of create, or newObject in the case of
	// delete) or an object that cannot have labels (like a
	// DeploymentRollback or a PodProxyOptions object) is not considered to
	// match.
	// Use the object selector only if the webhook is opt-in, because end
	// users may skip the admission webhook by setting the labels.
	// Default to the empty LabelSelector, which matches everything.
	ObjectSelector *metav1.LabelSelector `json:"objectSelector,omitempty"`
}

func (p *PolicySpec) Defaults() {
	if p.Mode == nil {
		protect := PolicyModeProtect
		p.Mode = &protect
	}

	if p.FailurePolicy == nil {
		fail := admissionregistrationv1.Fail
		p.FailurePolicy = &fail
	}

	if p.MatchPolicy == nil {
		equivalent := admissionregistrationv1.Equivalent
		p.MatchPolicy = &equivalent
	}

	if p.NamespaceSelector == nil {
		p.NamespaceSelector = &metav1.LabelSelector{}
	}

	if p.ObjectSelector == nil {
		p.ObjectSelector = &metav1.LabelSelector{}
	}
}

func newSettings(reader io.Reader) (Settings, error) {
	data, err := io.ReadAll(reader)
	if err != nil {
		return Settings{}, err
	}

	var settings Settings
	err = yaml.Unmarshal(data, &settings)
	if err != nil {
		return Settings{}, err
	}

	for name, policy := range settings.Policies {
		policy.Defaults()
		settings.Policies[name] = policy
	}

	return settings, nil
}
