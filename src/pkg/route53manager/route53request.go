package route53manager

import (
	"context"
	"log/slog"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/aws/aws-sdk-go-v2/service/route53/types"
	"github.com/pkg/errors"
)

// LowestRecordTTL is the minimum TTL Route 53 accepts for a standard resource record set.
const LowestRecordTTL int64 = 0

type changeClient interface {
	ChangeResourceRecordSets(ctx context.Context, params *route53.ChangeResourceRecordSetsInput, optFns ...func(*route53.Options)) (*route53.ChangeResourceRecordSetsOutput, error)
}

func route53Request(ctx context.Context, action types.ChangeAction, data route53RequestData, client changeClient, logger *slog.Logger) error {
	logger.DebugContext(ctx, "requesting route53 change",
		"action", action,
		"name", data.recordName,
		"value", data.recordValue,
		"ttl", data.recordTtl,
		"type", data.recordType,
		"comment", data.recordComment,
		"hostedZoneId", data.hostedZoneId)

	input := &route53.ChangeResourceRecordSetsInput{
		ChangeBatch: &types.ChangeBatch{
			Changes: []types.Change{
				{
					Action: action,
					ResourceRecordSet: &types.ResourceRecordSet{
						Name: aws.String(data.recordName),
						ResourceRecords: []types.ResourceRecord{
							{
								Value: aws.String(data.recordValue),
							},
						},
						TTL:  aws.Int64(data.recordTtl),
						Type: types.RRType(data.recordType),
					},
				},
			},
			Comment: aws.String(data.recordComment),
		},
		HostedZoneId: aws.String(data.hostedZoneId),
	}

	_, err := client.ChangeResourceRecordSets(ctx, input)
	if err != nil {
		if action == types.ChangeActionDelete && isRecordNotFound(err) {
			logger.InfoContext(ctx, "route53 record already absent",
				"name", data.recordName,
				"hostedZoneId", data.hostedZoneId)
			return nil
		}
		logger.ErrorContext(ctx, "error performing route53 change Request", "action", action, "err", err)
		return errors.Wrapf(err, "error performing route53 %s Request", action)
	}

	logger.InfoContext(ctx, "route53 record changed",
		"action", action,
		"name", data.recordName,
		"value", data.recordValue,
		"hostedZoneId", data.hostedZoneId)
	return nil
}

func isRecordNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not found")
}

type route53RequestData struct {
	recordName    string
	recordValue   string
	recordTtl     int64
	recordType    string
	recordComment string
	hostedZoneId  string
}
