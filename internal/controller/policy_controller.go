/*
Copyright 2024.

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

package controller

import (
	"context"
	"fmt"

	"github.com/pkg/errors"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/protoadapt"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1 "github.com/stackrox/rox/generated/api/v1"
	storage "github.com/stackrox/rox/generated/storage"
	"github.com/stackrox/rox/roxctl/common"
	"github.com/stackrox/rox/roxctl/common/auth"
	roxctlIO "github.com/stackrox/rox/roxctl/common/io"
	"github.com/stackrox/rox/roxctl/common/logger"
	"github.com/stackrox/rox/roxctl/common/printer"

	configstackroxiov1alpha1 "github.com/kylape/policy-controller/api/v1alpha1"
)

// PolicyReconciler reconciles a Policy object
type PolicyReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=config.stackrox.io.redhat.com,resources=policies,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=config.stackrox.io.redhat.com,resources=policies/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=config.stackrox.io.redhat.com,resources=policies/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Policy object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.16.3/pkg/reconcile
func (r *PolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = log.FromContext(ctx)

	// Get the policy CR
	policyCR := &configstackroxiov1alpha1.Policy{}
	if err := r.Client.Get(ctx, req.NamespacedName, policyCR); err != nil {
		if k8serr.IsNotFound(err) {
			// Must have been deleted
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("Failed to get policy: namespace=%s, name=%s", req.Namespace, req.Name)
	}

	desiredState := &storage.Policy{}
	if err := protojson.Unmarshal([]byte(policyCR.Spec.Policy), protoadapt.MessageV2Of(desiredState)); err != nil {
		return ctrl.Result{}, errors.Wrap(err, "Failed to parse policy JSON")
	}

	// GET policy from Central
	defaultIO := roxctlIO.DefaultIO()
	conn, err := common.GetGRPCConnection(auth.TokenAuth(), logger.NewLogger(defaultIO, printer.DefaultColorPrinter()))
	if err != nil {
		return ctrl.Result{}, errors.Wrap(err, "could not establish gRPC connection to central")
	}
	svc := v1.NewPolicyServiceClient(conn)
	allPolicies, err := svc.ListPolicies(ctx, &v1.RawQuery{})

	if err != nil {
		return ctrl.Result{}, errors.Wrap(err, "Failed to list all policies")
	}

	for _, policy := range allPolicies.Policies {
		if policy.Name == req.Name {
			desiredState.Id = policy.Id
			if _, err = svc.PutPolicy(ctx, desiredState); err != nil {
				return ctrl.Result{}, errors.Wrap(err, fmt.Sprintf("Failed to PUT policy %s", req.Name))
			}
			return ctrl.Result{}, nil
		}
	}

	if _, err = svc.PostPolicy(ctx, &v1.PostPolicyRequest{Policy: desiredState}); err != nil {
		wrappedErr := errors.Wrap(err, fmt.Sprintf("Failed to create policy %s", req.Name))
		policyCR.Status.Accepted = false
		policyCR.Status.Message = wrappedErr.Error()
		r.Client.Status().Update(ctx, policyCR)
		return ctrl.Result{}, wrappedErr
	} else {
		policyCR.Status.Accepted = true
		policyCR.Status.Message = "Successfully updated policy"
		r.Client.Status().Update(ctx, policyCR)
	}

	// Report status

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *PolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&configstackroxiov1alpha1.Policy{}).
		Complete(r)
}
