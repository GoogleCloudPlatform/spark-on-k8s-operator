/*
Copyright 2017 Google LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    https://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	apiv1 "k8s.io/api/core/v1"
	apiextensionsfake "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/fake"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubeclientfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"

	"k8s.io/spark-on-k8s-operator/pkg/apis/sparkoperator.k8s.io/v1alpha1"
	crdfake "k8s.io/spark-on-k8s-operator/pkg/client/clientset/versioned/fake"
)

func newFakeController() (*SparkApplicationController, *record.FakeRecorder) {
	crdClient := crdfake.NewSimpleClientset()
	kubeClient := kubeclientfake.NewSimpleClientset()
	apiExtensionsClient := apiextensionsfake.NewSimpleClientset()
	kubeClient.CoreV1().Nodes().Create(&apiv1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "node1",
		},
		Status: apiv1.NodeStatus{
			Addresses: []apiv1.NodeAddress{
				{
					Type:    apiv1.NodeExternalIP,
					Address: "12.34.56.78",
				},
			},
		},
	})

	recorder := record.NewFakeRecorder(3)
	return newSparkApplicationController(crdClient, kubeClient, apiExtensionsClient,
		recorder, 0), recorder
}

func TestSubmitApp(t *testing.T) {
	ctrl, _ := newFakeController()

	os.Setenv(kubernetesServiceHostEnvVar, "localhost")
	os.Setenv(kubernetesServicePortEnvVar, "443")

	app := &v1alpha1.SparkApplication{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "default",
		},
	}
	ctrl.crdClient.SparkoperatorV1alpha1().SparkApplications(app.Namespace).Create(app)

	go ctrl.submitApp(app)
	submission := <-ctrl.runner.queue
	assert.Equal(t, app.Name, submission.name)
	assert.Equal(t, app.Namespace, submission.namespace)
}

func TestOnAdd(t *testing.T) {
	ctrl, recorder := newFakeController()

	app := &v1alpha1.SparkApplication{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "default",
		},
		Status: v1alpha1.SparkApplicationStatus{
			AppID: "foo-123",
		},
	}
	ctrl.onAdd(app)

	item, _ := ctrl.queue.Get()
	defer ctrl.queue.Done(item)
	key, ok := item.(string)
	assert.True(t, ok)
	expectedKey, _ := cache.MetaNamespaceKeyFunc(app)
	assert.Equal(t, expectedKey, key)
	ctrl.queue.Forget(item)

	assert.Equal(t, 1, len(recorder.Events))
	event := <-recorder.Events
	assert.True(t, strings.Contains(event, "SparkApplicationSubmission"))
}

func TestOnUpdate(t *testing.T) {
	ctrl, recorder := newFakeController()

	type testcase struct {
		name          string
		oldApp        *v1alpha1.SparkApplication
		newApp        *v1alpha1.SparkApplication
		expectEnqueue bool
	}

	testFn := func(t *testing.T, test testcase) {
		ctrl.onUpdate(test.oldApp, test.newApp)
		if test.expectEnqueue {
			item, _ := ctrl.queue.Get()
			defer ctrl.queue.Done(item)
			key, ok := item.(string)
			assert.True(t, ok)
			expectedKey, _ := cache.MetaNamespaceKeyFunc(test.newApp)
			assert.Equal(t, expectedKey, key)
			ctrl.queue.Forget(item)

			event := <-recorder.Events
			assert.True(t, strings.Contains(event, "SparkApplicationSubmission"))
		} else {
			assert.Equal(t, 0, ctrl.queue.Len())
			assert.Equal(t, 0, len(recorder.Events))
		}
	}

	appTemplate := v1alpha1.SparkApplication{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "default",
		},
		Spec: v1alpha1.SparkApplicationSpec{
			Mode:  v1alpha1.ClusterMode,
			Image: stringptr("foo-image:v1"),
			Executor: v1alpha1.ExecutorSpec{
				Instances: int32ptr(1),
			},
		},
	}

	copyWithDifferentImage := appTemplate.DeepCopy()
	copyWithDifferentImage.Spec.Image = stringptr("foo-image:v2")

	copyWithDifferentExecutorInstances := appTemplate.DeepCopy()
	copyWithDifferentExecutorInstances.Spec.Executor.Instances = int32ptr(3)

	testcases := []testcase{
		{
			name:          "update with identical spec",
			oldApp:        appTemplate.DeepCopy(),
			newApp:        appTemplate.DeepCopy(),
			expectEnqueue: false,
		},
		{
			name:          "update with different image",
			oldApp:        appTemplate.DeepCopy(),
			newApp:        copyWithDifferentImage,
			expectEnqueue: true,
		},
		{
			name:          "update with different executor instances",
			oldApp:        appTemplate.DeepCopy(),
			newApp:        copyWithDifferentExecutorInstances,
			expectEnqueue: true,
		},
	}

	for _, test := range testcases {
		testFn(t, test)
	}
}

func TestOnDelete(t *testing.T) {
	ctrl, recorder := newFakeController()

	app := &v1alpha1.SparkApplication{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "default",
		},
		Status: v1alpha1.SparkApplicationStatus{
			AppID: "foo-123",
		},
	}
	ctrl.onAdd(app)
	ctrl.queue.Get()
	<-recorder.Events

	ctrl.onDelete(app)
	ctrl.queue.ShutDown()
	item, _ := ctrl.queue.Get()
	defer ctrl.queue.Done(item)
	assert.True(t, item == nil)
	event := <-recorder.Events
	assert.True(t, strings.Contains(event, "SparkApplicationDeletion"))
	ctrl.queue.Forget(item)
}

func TestProcessSingleDriverStateUpdate(t *testing.T) {
	type testcase struct {
		name             string
		update           driverStateUpdate
		expectedAppState v1alpha1.ApplicationStateType
	}

	ctrl, recorder := newFakeController()

	app := &v1alpha1.SparkApplication{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "default",
		},
		Status: v1alpha1.SparkApplicationStatus{
			AppID: "foo-123",
			AppState: v1alpha1.ApplicationState{
				State:        v1alpha1.NewState,
				ErrorMessage: "",
			},
		},
	}
	ctrl.crdClient.SparkoperatorV1alpha1().SparkApplications(app.Namespace).Create(app)
	ctrl.store.Add(app)

	testcases := []testcase{
		{
			name: "succeeded driver",
			update: driverStateUpdate{
				appName:      "foo",
				appNamespace: "default",
				appID:        "foo-123",
				podName:      "foo-driver",
				nodeName:     "node1",
				podPhase:     apiv1.PodSucceeded,
			},
			expectedAppState: v1alpha1.CompletedState,
		},
		{
			name: "failed driver",
			update: driverStateUpdate{
				appName:      "foo",
				appNamespace: "default",
				appID:        "foo-123",
				podName:      "foo-driver",
				nodeName:     "node1",
				podPhase:     apiv1.PodFailed,
			},
			expectedAppState: v1alpha1.FailedState,
		},
		{
			name: "running driver",
			update: driverStateUpdate{
				appName:      "foo",
				appNamespace: "default",
				appID:        "foo-123",
				podName:      "foo-driver",
				nodeName:     "node1",
				podPhase:     apiv1.PodRunning,
			},
			expectedAppState: v1alpha1.RunningState,
		},
	}

	testFn := func(test testcase, t *testing.T) {
		ctrl.processSingleDriverStateUpdate(&test.update)
		app, err := ctrl.crdClient.SparkoperatorV1alpha1().SparkApplications(app.Namespace).Get(app.Name,
			metav1.GetOptions{})
		if err != nil {
			t.Error(err)
		}
		assert.Equal(
			t,
			test.expectedAppState,
			app.Status.AppState.State,
			"wanted application state %s got %s",
			test.expectedAppState,
			app.Status.AppState.State)

		if isAppTerminated(app.Status.AppState.State) {
			event := <-recorder.Events
			assert.True(t, strings.Contains(event, "SparkApplicationTermination"))
		}

		ctrl.store.Update(app)
	}

	for _, test := range testcases {
		testFn(test, t)
	}
}

func TestProcessSingleAppStateUpdate(t *testing.T) {
	type testcase struct {
		name             string
		update           appStateUpdate
		initialAppState  v1alpha1.ApplicationStateType
		expectedAppState v1alpha1.ApplicationStateType
	}

	ctrl, recorder := newFakeController()

	app := &v1alpha1.SparkApplication{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "default",
		},
		Spec: v1alpha1.SparkApplicationSpec{
			MaxSubmissionRetries: int32ptr(1),
			SubmissionRetryInterval: int64ptr(300),
		},
		Status: v1alpha1.SparkApplicationStatus{
			AppID: "foo-123",
			AppState: v1alpha1.ApplicationState{
				State:        v1alpha1.NewState,
				ErrorMessage: "",
			},
		},
	}
	ctrl.crdClient.SparkoperatorV1alpha1().SparkApplications(app.Namespace).Create(app)
	ctrl.store.Add(app)

	testcases := []testcase{
		{
			name: "succeeded app",
			update: appStateUpdate{
				namespace:    "default",
				name:         "foo",
				state:        v1alpha1.CompletedState,
				errorMessage: "",
			},
			initialAppState:  v1alpha1.RunningState,
			expectedAppState: v1alpha1.CompletedState,
		},
		{
			name: "failed app",
			update: appStateUpdate{
				namespace:    "default",
				name:         "foo",
				state:        v1alpha1.FailedState,
				errorMessage: "",
			},
			initialAppState:  v1alpha1.RunningState,
			expectedAppState: v1alpha1.FailedState,
		},
		{
			name: "running app",
			update: appStateUpdate{
				namespace:    "default",
				name:         "foo",
				state:        v1alpha1.RunningState,
				errorMessage: "",
			},
			initialAppState:  v1alpha1.NewState,
			expectedAppState: v1alpha1.RunningState,
		},
		{
			name: "completed app with initial state SubmittedState",
			update: appStateUpdate{
				namespace:    "default",
				name:         "foo",
				state:        v1alpha1.FailedSubmissionState,
				errorMessage: "",
			},
			initialAppState:  v1alpha1.RunningState,
			expectedAppState: v1alpha1.FailedSubmissionState,
		},
	}

	testFn := func(test testcase, t *testing.T) {
		ctrl.processSingleAppStateUpdate(&test.update)
		app, err := ctrl.crdClient.SparkoperatorV1alpha1().SparkApplications(app.Namespace).Get(app.Name,
			metav1.GetOptions{})
		if err != nil {
			t.Error(err)
		}
		assert.Equal(
			t,
			test.expectedAppState,
			app.Status.AppState.State,
			"wanted application state %s got %s",
			test.expectedAppState,
			app.Status.AppState.State)

		if app.Status.AppState.State == v1alpha1.FailedSubmissionState {
			event := <-recorder.Events
			assert.True(t, strings.Contains(event, "SparkApplicationSubmissionFailure"))
		}

		ctrl.store.Update(app)
	}

	for _, test := range testcases {
		testFn(test, t)
	}
}

func TestProcessSingleExecutorStateUpdate(t *testing.T) {
	type testcase struct {
		name                   string
		update                 executorStateUpdate
		expectedExecutorStates map[string]v1alpha1.ExecutorState
	}

	ctrl, _ := newFakeController()

	app := &v1alpha1.SparkApplication{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "default",
		},
		Status: v1alpha1.SparkApplicationStatus{
			AppID: "foo-123",
			AppState: v1alpha1.ApplicationState{
				State:        v1alpha1.NewState,
				ErrorMessage: "",
			},
			ExecutorState: make(map[string]v1alpha1.ExecutorState),
		},
	}
	ctrl.crdClient.SparkoperatorV1alpha1().SparkApplications(app.Namespace).Create(app)
	ctrl.store.Add(app)

	testcases := []testcase{
		{
			name: "completed executor",
			update: executorStateUpdate{
				appNamespace: "default",
				appName:      "foo",
				appID:        "foo-123",
				podName:      "foo-exec-1",
				executorID:   "1",
				state:        v1alpha1.ExecutorCompletedState,
			},
			expectedExecutorStates: map[string]v1alpha1.ExecutorState{
				"foo-exec-1": v1alpha1.ExecutorCompletedState,
			},
		},
		{
			name: "failed executor",
			update: executorStateUpdate{
				appNamespace: "default",
				appName:      "foo",
				appID:        "foo-123",
				podName:      "foo-exec-2",
				executorID:   "2",
				state:        v1alpha1.ExecutorFailedState,
			},
			expectedExecutorStates: map[string]v1alpha1.ExecutorState{
				"foo-exec-1": v1alpha1.ExecutorCompletedState,
				"foo-exec-2": v1alpha1.ExecutorFailedState,
			},
		},
		{
			name: "running executor",
			update: executorStateUpdate{
				appNamespace: "default",
				appName:      "foo",
				appID:        "foo-123",
				podName:      "foo-exec-3",
				executorID:   "3",
				state:        v1alpha1.ExecutorRunningState,
			},
			expectedExecutorStates: map[string]v1alpha1.ExecutorState{
				"foo-exec-1": v1alpha1.ExecutorCompletedState,
				"foo-exec-2": v1alpha1.ExecutorFailedState,
				"foo-exec-3": v1alpha1.ExecutorRunningState,
			},
		},
		{
			name: "pending state update for a terminated executor",
			update: executorStateUpdate{
				appNamespace: "default",
				appName:      "foo",
				appID:        "foo-123",
				podName:      "foo-exec-1",
				executorID:   "1",
				state:        v1alpha1.ExecutorPendingState,
			},
			expectedExecutorStates: map[string]v1alpha1.ExecutorState{
				"foo-exec-1": v1alpha1.ExecutorCompletedState,
				"foo-exec-2": v1alpha1.ExecutorFailedState,
				"foo-exec-3": v1alpha1.ExecutorRunningState,
			},
		},
		{
			name: "pending executor",
			update: executorStateUpdate{
				appNamespace: "default",
				appName:      "foo",
				appID:        "foo-123",
				podName:      "foo-exec-4",
				executorID:   "4",
				state:        v1alpha1.ExecutorPendingState,
			},
			expectedExecutorStates: map[string]v1alpha1.ExecutorState{
				"foo-exec-1": v1alpha1.ExecutorCompletedState,
				"foo-exec-2": v1alpha1.ExecutorFailedState,
				"foo-exec-3": v1alpha1.ExecutorRunningState,
				"foo-exec-4": v1alpha1.ExecutorPendingState,
			},
		},
	}

	testFn := func(test testcase, t *testing.T) {
		ctrl.processSingleExecutorStateUpdate(&test.update)
		app, err := ctrl.crdClient.SparkoperatorV1alpha1().SparkApplications(app.Namespace).Get(app.Name,
			metav1.GetOptions{})
		if err != nil {
			t.Error(err)
		}
		assert.Equal(
			t,
			test.expectedExecutorStates,
			app.Status.ExecutorState,
			"wanted executor states %s got %s",
			test.expectedExecutorStates,
			app.Status.ExecutorState)

		ctrl.store.Update(app)
	}

	for _, test := range testcases {
		testFn(test, t)
	}
}

func TestHandleRestart(t *testing.T) {
	type testcase struct {
		name          string
		app           *v1alpha1.SparkApplication
		expectRestart bool
	}

	os.Setenv(kubernetesServiceHostEnvVar, "localhost")
	os.Setenv(kubernetesServicePortEnvVar, "443")

	ctrl, recorder := newFakeController()

	testFn := func(test testcase, t *testing.T) {
		ctrl.crdClient.SparkoperatorV1alpha1().SparkApplications(test.app.Namespace).Create(test.app)
		ctrl.store.Add(test.app)
		ctrl.handleRestart(test.app)
		if test.expectRestart {
			go ctrl.processNextItem()
			submission := <-ctrl.runner.queue
			assert.Equal(t, test.app.Name, submission.name)
			assert.Equal(t, test.app.Namespace, submission.namespace)
			event := <-recorder.Events
			assert.True(t, strings.Contains(event, "SparkApplicationRestart"))
		}
	}

	testcases := []testcase{
		{
			name: "completed application with restart policy Never",
			app: &v1alpha1.SparkApplication{
				ObjectMeta: metav1.ObjectMeta{Name: "foo1", Namespace: "default"},
				Spec:       v1alpha1.SparkApplicationSpec{RestartPolicy: v1alpha1.Never},
				Status: v1alpha1.SparkApplicationStatus{
					AppState: v1alpha1.ApplicationState{State: v1alpha1.CompletedState},
				},
			},
			expectRestart: false,
		},
		{
			name: "completed application with restart policy OnFailure",
			app: &v1alpha1.SparkApplication{
				ObjectMeta: metav1.ObjectMeta{Name: "foo2", Namespace: "default"},
				Spec:       v1alpha1.SparkApplicationSpec{RestartPolicy: v1alpha1.OnFailure},
				Status: v1alpha1.SparkApplicationStatus{
					AppState: v1alpha1.ApplicationState{State: v1alpha1.CompletedState},
				},
			},
			expectRestart: false,
		},
		{
			name: "completed application with restart policy Always",
			app: &v1alpha1.SparkApplication{
				ObjectMeta: metav1.ObjectMeta{Name: "foo3", Namespace: "default"},
				Spec:       v1alpha1.SparkApplicationSpec{RestartPolicy: v1alpha1.Always},
				Status: v1alpha1.SparkApplicationStatus{
					AppState: v1alpha1.ApplicationState{State: v1alpha1.CompletedState},
				},
			},
			expectRestart: true,
		},
		{
			name: "failed application with restart policy Never",
			app: &v1alpha1.SparkApplication{
				ObjectMeta: metav1.ObjectMeta{Name: "foo4", Namespace: "default"},
				Spec:       v1alpha1.SparkApplicationSpec{RestartPolicy: v1alpha1.Never},
				Status: v1alpha1.SparkApplicationStatus{
					AppState: v1alpha1.ApplicationState{State: v1alpha1.FailedState},
				},
			},
			expectRestart: false,
		},
		{
			name: "failed application with restart policy OnFailure",
			app: &v1alpha1.SparkApplication{
				ObjectMeta: metav1.ObjectMeta{Name: "foo5", Namespace: "default"},
				Spec:       v1alpha1.SparkApplicationSpec{RestartPolicy: v1alpha1.OnFailure},
				Status: v1alpha1.SparkApplicationStatus{
					AppState: v1alpha1.ApplicationState{State: v1alpha1.FailedState},
				},
			},
			expectRestart: true,
		},
		{
			name: "failed application with restart policy Always",
			app: &v1alpha1.SparkApplication{
				ObjectMeta: metav1.ObjectMeta{Name: "foo6", Namespace: "default"},
				Spec:       v1alpha1.SparkApplicationSpec{RestartPolicy: v1alpha1.Always},
				Status: v1alpha1.SparkApplicationStatus{
					AppState: v1alpha1.ApplicationState{State: v1alpha1.FailedState},
				},
			},
			expectRestart: true,
		},
	}

	for _, test := range testcases {
		testFn(test, t)
	}
}

func TestResubmissionOnFailures(t *testing.T) {
	ctrl, recorder := newFakeController()

	os.Setenv(kubernetesServiceHostEnvVar, "localhost")
	os.Setenv(kubernetesServicePortEnvVar, "443")

	app := &v1alpha1.SparkApplication{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "default",
		},
		Spec: v1alpha1.SparkApplicationSpec{
			MaxSubmissionRetries: int32ptr(2),
			SubmissionRetryInterval: int64ptr(1),
		},
		Status: v1alpha1.SparkApplicationStatus{
			AppID: "foo-123",
			AppState: v1alpha1.ApplicationState{
				State:        v1alpha1.NewState,
				ErrorMessage: "",
			},
		},
	}

	ctrl.crdClient.SparkoperatorV1alpha1().SparkApplications(app.Namespace).Create(app)
	ctrl.store.Add(app)

	testFn := func(t *testing.T, update *appStateUpdate) {
		ctrl.processSingleAppStateUpdate(update)
		item, _ := ctrl.queue.Get()
		key, ok := item.(string)
		assert.True(t, ok)
		expectedKey, _ := getApplicationKey(app.Namespace, app.Name)
		assert.Equal(t, expectedKey, key)
		ctrl.queue.Forget(item)
		ctrl.queue.Done(item)

		updatedApp, err := ctrl.crdClient.SparkoperatorV1alpha1().SparkApplications(app.Namespace).Get(app.Name,
			metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, int32(1), updatedApp.Status.SubmissionRetries)

		event := <-recorder.Events
		assert.True(t, strings.Contains(event, "SparkApplicationSubmissionFailure"))
		event = <-recorder.Events
		assert.True(t, strings.Contains(event, "SparkApplicationSubmissionRetry"))
	}

	update := &appStateUpdate{
		namespace: app.Namespace,
		name:      app.Name,
		state:     v1alpha1.FailedSubmissionState,
	}

	// First 2 failed submissions should result in re-submission attempts.
	testFn(t, update)
	testFn(t, update)

	// The next failed submission should not cause a re-submission attempt.
	ctrl.processSingleAppStateUpdate(update)
	assert.Equal(t, 0, ctrl.queue.Len())
}

func stringptr(s string) *string {
	return &s
}

func int64ptr(n int64) *int64 {
	return &n
}

func int32ptr(n int32) *int32 {
	return &n
}
