package queue

import (
	"context"
	"encoding/json"
	"fmt"

	cloudtasks "cloud.google.com/go/cloudtasks/apiv2"
	taskspb "cloud.google.com/go/cloudtasks/apiv2/cloudtaskspb"

	"github.com/mediusfy/modulex/services/prreview/webhook"
)

// CloudTasks is the production Enqueuer (ADR-0035 "Fast ack, async work"):
// one shared queue for all installations — per-PR serialization is the
// Firestore lease's job, not per-PR queues, which would hit Cloud Tasks'
// ~1,000-queues-per-project limit at scale. Tasks call the worker's Cloud
// Run URL with an OIDC token so only the queue's service account can
// invoke it.
type CloudTasks struct {
	Client *cloudtasks.Client
	// QueuePath is projects/{project}/locations/{location}/queues/{queue}.
	QueuePath string
	// WorkerURL is the worker Cloud Run service's task endpoint.
	WorkerURL string
	// InvokerServiceAccount signs the OIDC token the worker verifies.
	InvokerServiceAccount string
}

func (c *CloudTasks) Enqueue(ctx context.Context, req webhook.ReviewRequest) error {
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	_, err = c.Client.CreateTask(ctx, &taskspb.CreateTaskRequest{
		Parent: c.QueuePath,
		Task: &taskspb.Task{
			MessageType: &taskspb.Task_HttpRequest{
				HttpRequest: &taskspb.HttpRequest{
					HttpMethod: taskspb.HttpMethod_POST,
					Url:        c.WorkerURL,
					Headers:    map[string]string{"Content-Type": "application/json"},
					Body:       body,
					AuthorizationHeader: &taskspb.HttpRequest_OidcToken{
						OidcToken: &taskspb.OidcToken{
							ServiceAccountEmail: c.InvokerServiceAccount,
							Audience:            c.WorkerURL,
						},
					},
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("cloud tasks enqueue %s/%s#%d: %w", req.Owner, req.Repo, req.PRNumber, err)
	}
	return nil
}
