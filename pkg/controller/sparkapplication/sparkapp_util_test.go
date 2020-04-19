/*
Copyright 2020 Google LLC

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

package sparkapplication

import (
	"reflect"
	"testing"

	"github.com/GoogleCloudPlatform/spark-on-k8s-operator/pkg/apis/sparkoperator.k8s.io/v1beta2"
)

var expectedStatusString = `{
  "sparkApplicationId": "test-app",
  "submissionID": "test-app-submission",
  "lastSubmissionAttemptTime": null,
  "terminationTime": null,
  "driverInfo": {},
  "applicationState": {
    "state": "COMPLETED"
  },
  "executorState": {
    "executor-1": "COMPLETED"
  }
}`

func TestPrintStatus(t *testing.T) {
	status := &v1beta2.SparkApplicationStatus{
		SparkApplicationID: "test-app",
		SubmissionID:       "test-app-submission",
		AppState: v1beta2.ApplicationState{
			State: v1beta2.CompletedState,
		},
		ExecutorState: map[string]v1beta2.ExecutorState{
			"executor-1": v1beta2.ExecutorCompletedState,
		},
	}

	statusString, err := printStatus(status)
	if err != nil {
		t.Fail()
	}

	if statusString != expectedStatusString {
		t.Errorf("status string\n %s is different from expected status string\n %s", statusString, expectedStatusString)
	}
}

func Test_getResourceAnnotations(t *testing.T) {

	ingressclass_nginx := "nginx"

	app1 := &v1beta2.SparkApplication{
		Spec: v1beta2.SparkApplicationSpec{
			UIConfig: nil,
		},
	}

	app2 := &v1beta2.SparkApplication{
		Spec: v1beta2.SparkApplicationSpec{
			UIConfig: &v1beta2.UIConfig{IngressClass: &ingressclass_nginx},
		},
	}

	app3 := &v1beta2.SparkApplication{
		Spec: v1beta2.SparkApplicationSpec{
			UIConfig: &v1beta2.UIConfig{IngressClass: &ingressclass_nginx, EnableSSL: true, ForceSSL: true},
		},
	}

	type args struct {
		app *v1beta2.SparkApplication
	}
	tests := []struct {
		name string
		args args
		want map[string]string
	}{
		{
			args: args{
				app1,
			},
			want: map[string]string{},
		},
		{
			args: args{
				app2,
			},
			want: map[string]string{"kubernetes.io/ingressclass": "nginx"},
		},
		{
			args: args{
				app3,
			},
			want: map[string]string{"kubernetes.io/ingressclass": "nginx", "nginx.ingress.kubernetes.io/force-ssl-redirect": "true"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := getResourceAnnotations(tt.args.app); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("getResourceAnnotations() = %v, want %v", got, tt.want)
			}
		})
	}
}