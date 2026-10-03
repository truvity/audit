package auditpulumi

import (
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/cloudwatch"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/lambda"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/sns"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/sqs"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// alarmTargets are what the alarms watch.
type alarmTargets struct {
	Queue, Dlq     *sqs.Queue
	Writer, Notary *lambda.Function
}

// AlarmNames lists the CloudWatch alarms the library creates, as the suffix after
// `<name>-`: the alarm set the AWS design calls for. Each publishes to the alarm topic on
// ALARM and on OK, and the topic is subscribed to alert-ingress over HTTPS.
//
//	writer-throttles, notary-throttles   Lambda throttled an invocation: the writer is not keeping up, or the account's concurrency is spent
//	ingest-dlq-not-empty                 a message was delivered MaxReceiveCount times and moved aside: a record is not in the archive
//	ingest-oldest-message-age            the oldest message in the queue is older than the threshold: the writer is behind or stopped
//	writer-errors, notary-errors         an invocation failed
//	notary-silent                        the notary has not been invoked for NotarySilenceHours: the schedule or the function is gone
//
// "Silent on OTLP" is the notary's silence, not a metric of the OTLP path
// itself: a function that stops being invoked sends nothing, and an alarm on the
// platform's own Invocations metric is the one that does not depend on the thing
// that is broken.
var AlarmNames = []string{
	"writer-throttles", "notary-throttles", "ingest-dlq-not-empty", "ingest-oldest-message-age",
	"writer-errors", "notary-errors", "notary-silent",
}

func newAlarms(ctx *pulumi.Context, name string, a *Args, t alarmTargets, tags pulumi.StringMap, opts ...pulumi.ResourceOption) (*sns.Topic, error) {
	// The topic is not encrypted with a customer key: CloudWatch could not publish
	// to one without a key policy of its own, and an alarm's body names a queue and
	// a function and carries no record.
	topic, err := sns.NewTopic(ctx, name+"-alarms", &sns.TopicArgs{Name: pulumi.String(name + "-alarms"), Tags: tags}, opts...)
	if err != nil {
		return nil, err
	}
	if a.Alerts.EndpointURL != nil {
		// alert-ingress must confirm: SNS POSTs a SubscriptionConfirmation to the
		// URL, and the subscription stays pending until it is followed.
		if _, err := sns.NewTopicSubscription(ctx, name+"-alert-ingress", &sns.TopicSubscriptionArgs{
			Topic:                topic.Arn,
			Protocol:             pulumi.String("https"),
			Endpoint:             a.Alerts.EndpointURL,
			EndpointAutoConfirms: pulumi.Bool(false),
		}, opts...); err != nil {
			return nil, err
		}
	}
	actions := pulumi.Array{topic.Arn}

	type alarm struct {
		suffix, desc, namespace, metric, stat, operator string
		dims                                            pulumi.StringMap
		threshold                                       float64
		period, evaluations, datapoints                 int
		missing                                         string
	}
	fnDims := func(f *lambda.Function) pulumi.StringMap { return pulumi.StringMap{"FunctionName": f.Name} }
	queueDims := func(q *sqs.Queue) pulumi.StringMap { return pulumi.StringMap{"QueueName": q.Name} }
	alarms := []alarm{
		{"writer-throttles", "The writer was throttled: it is not keeping up, or the account's Lambda concurrency is spent.",
			"AWS/Lambda", "Throttles", "Sum", "GreaterThanThreshold", fnDims(t.Writer), 0, 300, 1, 1, "notBreaching"},
		{"notary-throttles", "The notary was throttled.",
			"AWS/Lambda", "Throttles", "Sum", "GreaterThanThreshold", fnDims(t.Notary), 0, 300, 1, 1, "notBreaching"},
		{"ingest-dlq-not-empty", "A message reached the dead-letter queue: a record was delivered the allowed number of times and is not in the archive.",
			"AWS/SQS", "ApproximateNumberOfMessagesVisible", "Maximum", "GreaterThanThreshold", queueDims(t.Dlq), 0, 300, 1, 1, "notBreaching"},
		{"ingest-oldest-message-age", "The oldest message in the ingest queue is older than the threshold: the writer is behind or not running.",
			"AWS/SQS", "ApproximateAgeOfOldestMessage", "Maximum", "GreaterThanThreshold", queueDims(t.Queue),
			float64(a.Alerts.OldestMessageAgeSeconds), 300, 1, 1, "notBreaching"},
		{"writer-errors", "A writer invocation failed.",
			"AWS/Lambda", "Errors", "Sum", "GreaterThanThreshold", fnDims(t.Writer), 0, 300, 1, 1, "notBreaching"},
		// The notary runs once an hour, so an hour is its natural period.
		{"notary-errors", "A notary run failed: a tenant could not be sealed, or the signer failed.",
			"AWS/Lambda", "Errors", "Sum", "GreaterThanThreshold", fnDims(t.Notary), 0, 3600, 1, 1, "notBreaching"},
		// Lambda publishes no Invocations datapoint for an hour with none, so the
		// missing data IS the signal: treat it as breaching, and every one of the
		// last NotarySilenceHours hours must be silent.
		{"notary-silent", "The notary has not been invoked for the silence window: the schedule or the function is gone, and the chain of seals is growing a gap.",
			"AWS/Lambda", "Invocations", "Sum", "LessThanThreshold", fnDims(t.Notary), 1, 3600,
			a.Alerts.NotarySilenceHours, a.Alerts.NotarySilenceHours, "breaching"},
	}
	for _, al := range alarms {
		if _, err := cloudwatch.NewMetricAlarm(ctx, name+"-"+al.suffix, &cloudwatch.MetricAlarmArgs{
			Name:               pulumi.String(name + "-" + al.suffix),
			AlarmDescription:   pulumi.String(al.desc),
			Namespace:          pulumi.String(al.namespace),
			MetricName:         pulumi.String(al.metric),
			Dimensions:         al.dims,
			Statistic:          pulumi.String(al.stat),
			ComparisonOperator: pulumi.String(al.operator),
			Threshold:          pulumi.Float64(al.threshold),
			Period:             pulumi.Int(al.period),
			EvaluationPeriods:  pulumi.Int(al.evaluations),
			DatapointsToAlarm:  pulumi.Int(al.datapoints),
			TreatMissingData:   pulumi.String(al.missing),
			AlarmActions:       actions,
			OkActions:          actions,
			Tags:               tags,
		}, opts...); err != nil {
			return nil, err
		}
	}
	return topic, nil
}
