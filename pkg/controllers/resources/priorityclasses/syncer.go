package priorityclasses

import (
	"context"
	context2 "github.com/loft-sh/vcluster/cmd/vcluster/context"
	"github.com/loft-sh/vcluster/pkg/constants"
	"github.com/loft-sh/vcluster/pkg/util/loghelper"
	"github.com/loft-sh/vcluster/pkg/util/translate"
	schedulingv1 "k8s.io/api/scheduling/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"time"
)

func RegisterSyncer(ctx *context2.ControllerContext) error {
	// build syncer and register it
	s := &syncer{
		targetNamespace: ctx.Options.TargetNamespace,
		virtualClient:   ctx.VirtualManager.GetClient(),
		localClient:     ctx.LocalManager.GetClient(),
	}
	err := ctx.VirtualManager.GetFieldIndexer().IndexField(ctx.Context, &schedulingv1.PriorityClass{}, constants.IndexByVName, func(rawObj client.Object) []string {
		metaAccessor, err := meta.Accessor(rawObj)
		if err != nil {
			return nil
		}

		return []string{s.priorityClassName(metaAccessor.GetName())}
	})
	if err != nil {
		return err
	}

	return registerForwardSyncer(ctx, s, "priorityclass", func(obj runtime.Object) bool {
		m, err := meta.Accessor(obj)
		if err != nil {
			return false
		}

		labels := m.GetLabels()
		if labels == nil {
			return false
		}

		return labels[translate.MarkerLabel] == s.markerLabel()
	}, s.priorityClassName)
}

type syncer struct {
	targetNamespace string
	localClient     client.Client
	virtualClient   client.Client
}

func (s *syncer) New() client.Object {
	return &schedulingv1.PriorityClass{}
}

func (s *syncer) NewList() client.ObjectList {
	return &schedulingv1.PriorityClassList{}
}

func (s *syncer) ForwardCreate(ctx context.Context, vObj client.Object, log loghelper.Logger) (ctrl.Result, error) {
	vPriorityClass := vObj.(*schedulingv1.PriorityClass)
	newPriorityClass, err := s.translate(vObj)
	if err != nil {
		return ctrl.Result{}, err
	}

	log.Infof("create physical priority class %s", newPriorityClass.Name)
	err = s.localClient.Create(ctx, newPriorityClass)
	if err != nil {
		log.Infof("error syncing %s to physical cluster: %v", vPriorityClass.Name, err)
		return ctrl.Result{RequeueAfter: time.Second}, err
	}

	return ctrl.Result{}, nil
}

func (s *syncer) ForwardCreateNeeded(vObj client.Object) (bool, error) {
	return true, nil
}

func (s *syncer) ForwardUpdate(ctx context.Context, pObj client.Object, vObj client.Object, log loghelper.Logger) (ctrl.Result, error) {
	// did the priority class change?
	pPriorityClass := pObj.(*schedulingv1.PriorityClass)
	vPriorityClass := vObj.(*schedulingv1.PriorityClass)
	updated, err := s.calcPriorityClassDiff(pPriorityClass, vPriorityClass)
	if err != nil {
		return ctrl.Result{}, err
	}
	if updated != nil {
		log.Infof("updating physical priority class %s, because virtual priority class has changed", updated.Name)
		err := s.localClient.Update(ctx, updated)
		if err != nil {
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

func (s *syncer) ForwardUpdateNeeded(pObj client.Object, vObj client.Object) (bool, error) {
	updated, err := s.calcPriorityClassDiff(pObj.(*schedulingv1.PriorityClass), vObj.(*schedulingv1.PriorityClass))
	return updated != nil, err
}

func (s *syncer) translate(vObj runtime.Object) (*schedulingv1.PriorityClass, error) {
	target := vObj.DeepCopyObject()
	m, err := meta.Accessor(target)
	if err != nil {
		return nil, err
	}

	// reset metadata & translate name
	translate.ResetObjectMetadata(m)
	m.SetName(s.priorityClassName(m.GetName()))

	// set marker label
	labels := map[string]string{}
	labels[translate.MarkerLabel] = s.markerLabel()
	m.SetLabels(labels)

	// translate the priority class
	priorityClass := target.(*schedulingv1.PriorityClass)
	priorityClass.GlobalDefault = false
	if priorityClass.Value > 1000000000 {
		priorityClass.Value = 1000000000
	}
	return priorityClass, nil
}

func (s *syncer) calcPriorityClassDiff(pObj, vObj *schedulingv1.PriorityClass) (*schedulingv1.PriorityClass, error) {
	var updated *schedulingv1.PriorityClass

	// check subsets
	if !equality.Semantic.DeepEqual(vObj.PreemptionPolicy, pObj.PreemptionPolicy) {
		updated = pObj.DeepCopy()
		updated.PreemptionPolicy = vObj.PreemptionPolicy
	}

	// check annotations
	if !equality.Semantic.DeepEqual(vObj.Annotations, pObj.Annotations) {
		if updated == nil {
			updated = pObj.DeepCopy()
		}
		updated.Annotations = vObj.Annotations
	}

	// check labels
	if !translate.LabelsEqual(vObj.Namespace, vObj.Labels, pObj.Labels) {
		if updated == nil {
			updated = pObj.DeepCopy()
		}
		updated.Labels = translate.TranslateLabels(vObj.Namespace, vObj.Labels)
	}

	// check description
	if vObj.Description != pObj.Description {
		if updated == nil {
			updated = pObj.DeepCopy()
		}
		updated.Description = vObj.Description
	}

	// check value
	translatedValue := vObj.Value
	if translatedValue > 1000000000 {
		translatedValue = 1000000000
	}
	if translatedValue != pObj.Value {
		if updated == nil {
			updated = pObj.DeepCopy()
		}
		updated.Value = translatedValue
	}

	return updated, nil
}

func (s *syncer) BackwardUpdate(ctx context.Context, pObj client.Object, vObj client.Object, log loghelper.Logger) (ctrl.Result, error) {
	return ctrl.Result{}, nil
}

func (s *syncer) BackwardUpdateNeeded(pObj client.Object, vObj client.Object) (bool, error) {
	return false, nil
}

func (s *syncer) markerLabel() string {
	return translate.SafeConcatName(s.targetNamespace, "x", translate.Suffix)
}

func (s *syncer) priorityClassName(name string) string {
	return TranslatePriorityClassName(name, s.targetNamespace)
}

func TranslatePriorityClassName(name, namespace string) string {
	// we have to prefix with vcluster as system is reserved
	return translate.SafeConcatName("vcluster", name, "x", namespace, "x", translate.Suffix)
}