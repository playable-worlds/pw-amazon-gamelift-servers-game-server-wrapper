package route53manager

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/aws/aws-sdk-go-v2/service/route53/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockChangeClient struct {
	calls []route53.ChangeResourceRecordSetsInput
	err   error
}

func (m *mockChangeClient) ChangeResourceRecordSets(ctx context.Context, params *route53.ChangeResourceRecordSetsInput, optFns ...func(*route53.Options)) (*route53.ChangeResourceRecordSetsOutput, error) {
	if params != nil {
		m.calls = append(m.calls, *params)
	}
	if m.err != nil {
		return nil, m.err
	}
	return &route53.ChangeResourceRecordSetsOutput{}, nil
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func sampleRecord(zoneId, value string) route53RequestData {
	return route53RequestData{
		recordName:    "session.worlds.example.com",
		recordValue:   value,
		recordTtl:     LowestRecordTTL,
		recordType:    "A",
		recordComment: "test",
		hostedZoneId:  zoneId,
	}
}

func TestRoute53RequestUpsertsWithLowestTTL(t *testing.T) {
	client := &mockChangeClient{}
	data := sampleRecord("ZPUBLIC", "1.2.3.4")

	err := route53Request(context.Background(), types.ChangeActionUpsert, data, client, testLogger())
	require.NoError(t, err)
	require.Len(t, client.calls, 1)

	change := client.calls[0].ChangeBatch.Changes[0]
	assert.Equal(t, types.ChangeActionUpsert, change.Action)
	assert.Equal(t, LowestRecordTTL, aws.ToInt64(change.ResourceRecordSet.TTL))
	assert.Equal(t, int64(0), aws.ToInt64(change.ResourceRecordSet.TTL))
}

func TestMappingCleanupDeletesPublicAndPrivateRecords(t *testing.T) {
	client := &mockChangeClient{}
	mapping := &Mapping{client: client}
	logger := testLogger()

	require.NoError(t, mapping.upsertRecord(context.Background(), logger, sampleRecord("ZPUBLIC", "1.2.3.4")))
	require.NoError(t, mapping.upsertRecord(context.Background(), logger, sampleRecord("ZPRIVATE", "10.0.0.1")))
	require.Len(t, client.calls, 2)

	err := mapping.Cleanup(context.Background(), logger)
	require.NoError(t, err)
	require.Len(t, client.calls, 4)

	publicDelete := client.calls[2].ChangeBatch.Changes[0]
	privateDelete := client.calls[3].ChangeBatch.Changes[0]
	assert.Equal(t, types.ChangeActionDelete, publicDelete.Action)
	assert.Equal(t, types.ChangeActionDelete, privateDelete.Action)
	assert.Equal(t, "ZPUBLIC", aws.ToString(client.calls[2].HostedZoneId))
	assert.Equal(t, "ZPRIVATE", aws.ToString(client.calls[3].HostedZoneId))
	assert.Equal(t, LowestRecordTTL, aws.ToInt64(publicDelete.ResourceRecordSet.TTL))
	assert.Equal(t, LowestRecordTTL, aws.ToInt64(privateDelete.ResourceRecordSet.TTL))
}

func TestMappingCleanupIsIdempotent(t *testing.T) {
	client := &mockChangeClient{}
	mapping := &Mapping{client: client}
	logger := testLogger()

	require.NoError(t, mapping.upsertRecord(context.Background(), logger, sampleRecord("ZPUBLIC", "1.2.3.4")))

	require.NoError(t, mapping.Cleanup(context.Background(), logger))
	require.NoError(t, mapping.Cleanup(context.Background(), logger))
	assert.Len(t, client.calls, 2) // one upsert, one delete
}

func TestRoute53DeleteTreatsNotFoundAsSuccess(t *testing.T) {
	client := &mockChangeClient{
		err: errors.New("InvalidChangeBatch: Tried to delete resource record set [name='session.worlds.example.com.', type='A'] but it was not found"),
	}

	err := route53Request(context.Background(), types.ChangeActionDelete, sampleRecord("ZPUBLIC", "1.2.3.4"), client, testLogger())
	assert.NoError(t, err)
}

func TestMappingNilCleanup(t *testing.T) {
	var mapping *Mapping
	assert.NoError(t, mapping.Cleanup(context.Background(), testLogger()))
}

func TestLowestRecordTTLIsRoute53Minimum(t *testing.T) {
	assert.Equal(t, int64(0), LowestRecordTTL)
}

func TestMappingCleanupUsesOnceUnderConcurrency(t *testing.T) {
	client := &mockChangeClient{}
	mapping := &Mapping{client: client}
	logger := testLogger()
	require.NoError(t, mapping.upsertRecord(context.Background(), logger, sampleRecord("ZPUBLIC", "1.2.3.4")))

	var started atomic.Int32
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func() {
			started.Add(1)
			_ = mapping.Cleanup(context.Background(), logger)
			done <- struct{}{}
		}()
	}
	for i := 0; i < 8; i++ {
		<-done
	}
	assert.Equal(t, int32(8), started.Load())
	assert.Len(t, client.calls, 2)
}
