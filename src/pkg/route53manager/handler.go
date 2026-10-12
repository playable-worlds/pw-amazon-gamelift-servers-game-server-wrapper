package route53manager

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	wrapperConfig "github.com/amazon-gamelift/amazon-gamelift-servers-game-server-wrapper/internal/config"
	"github.com/amazon-gamelift/amazon-gamelift-servers-game-server-wrapper/pkg/helpers"
	"github.com/amazon-gamelift/amazon-gamelift-servers-game-server-wrapper/pkg/types/events"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/aws/aws-sdk-go-v2/service/route53/types"
	"github.com/pkg/errors"
)

// Mapping tracks Route 53 records created for a game session so they can be deleted on shutdown.
type Mapping struct {
	client  changeClient
	records []route53RequestData
	once    sync.Once
}

func SetupRoute53Mappings(ctx context.Context, logger *slog.Logger, zoneId string, cfg *wrapperConfig.Config, creds *events.AwsCredentials, requestHandler *helpers.HttpRequestHandler) (*Mapping, error) {

	token, err := requestHandler.Request(ctx, helpers.HttpRequestDetails{
		Method:  "PUT",
		Url:     cfg.Route53.TokenUrl,
		Headers: map[string]string{cfg.Route53.TokenHeaderKey: cfg.Route53.TokenHeaderValue},
	})
	if err != nil {
		logger.ErrorContext(ctx, "error getting token", "err", err)
		return nil, errors.Wrap(err, "error getting token")
	}

	metaData, err := requestHandler.Request(ctx, helpers.HttpRequestDetails{
		Method:  "GET",
		Url:     cfg.Route53.MetaDataUrl,
		Headers: map[string]string{cfg.Route53.MetaDataHeaderKey: token},
	})
	if err != nil {
		logger.ErrorContext(ctx, "error getting metaData", "err", err)
		return nil, errors.Wrap(err, "error getting metadata")
	}

	publicIp, err := requestHandler.Request(ctx, helpers.HttpRequestDetails{
		Method:  "GET",
		Url:     strings.Replace(cfg.Route53.PublicIpUrl, "{{metaData}}", metaData, -1),
		Headers: map[string]string{cfg.Route53.MetaDataHeaderKey: token},
	})
	if err != nil {
		logger.ErrorContext(ctx, "error getting publicIp", "err", err)
		return nil, errors.Wrap(err, "error getting public ip")
	}

	privateIp, err := requestHandler.Request(ctx, helpers.HttpRequestDetails{
		Method:  "GET",
		Url:     strings.Replace(cfg.Route53.PrivateIpUrl, "{{metaData}}", metaData, -1),
		Headers: map[string]string{cfg.Route53.MetaDataHeaderKey: token},
	})
	if err != nil {
		logger.ErrorContext(ctx, "error getting privateIp", "err", err)
		return nil, errors.Wrap(err, "error getting private ip")
	}

	var r53Config aws.Config
	if creds != nil {
		r53Config, err = config.LoadDefaultConfig(ctx, config.WithRegion(cfg.Route53.Region), config.WithCredentialsProvider(credentials.StaticCredentialsProvider{
			Value: aws.Credentials{
				AccessKeyID: creds.AccessKeyId, SecretAccessKey: creds.SecretAccessKey, SessionToken: creds.SessionToken, Source: "Fleet Instance Role",
			},
		}))
		if err != nil {
			logger.ErrorContext(ctx, "failed to retrieve given r53 config", "err", err)
		}
	}
	if creds == nil || err != nil {
		r53Config, err = config.LoadDefaultConfig(ctx, config.WithRegion(cfg.Route53.Region))
		if err != nil {
			logger.ErrorContext(ctx, "error getting default r53 config", "err", err)
			return nil, errors.Wrap(err, "error getting default r53 config")
		}
	}

	client := route53.NewFromConfig(r53Config)
	recordName := fmt.Sprintf("%s.worlds.%s", zoneId, cfg.Route53.HostDomain) // TODO: Check format

	mapping := &Mapping{client: client}

	if cfg.Route53.PublicHostedZoneId != "" && publicIp != "" {
		err = mapping.upsertRecord(ctx, logger, route53RequestData{
			recordName:    recordName,
			recordValue:   publicIp,
			recordTtl:     LowestRecordTTL,
			recordType:    cfg.Route53.Type,
			recordComment: cfg.Route53.Comment,
			hostedZoneId:  cfg.Route53.PublicHostedZoneId,
		})
		if err != nil {
			logger.ErrorContext(ctx, "error performing route53 Request for public hosted zone", "err", err)
			return mapping, errors.Wrap(err, "error performing route53 Request for public hosted zone")
		}
	} else {
		logger.Debug("skipping route53 setup for public ip", "publicIp", publicIp, "public hosted zoneId", cfg.Route53.PublicHostedZoneId)
	}

	if cfg.Route53.PrivateHostedZoneId != "" && privateIp != "" {
		err = mapping.upsertRecord(ctx, logger, route53RequestData{
			recordName:    recordName,
			recordValue:   privateIp,
			recordTtl:     LowestRecordTTL,
			recordType:    cfg.Route53.Type,
			recordComment: cfg.Route53.Comment,
			hostedZoneId:  cfg.Route53.PrivateHostedZoneId,
		})
		if err != nil {
			logger.ErrorContext(ctx, "error performing route53 Request for private hosted zone", "err", err)
			return mapping, errors.Wrap(err, "error performing route53 Request for private hosted zone")
		}
	} else {
		logger.DebugContext(ctx, "skipping route53 setup for private ip", "privateIp", privateIp, "private hosted zoneId", cfg.Route53.PrivateHostedZoneId)
	}

	return mapping, nil
}

func (m *Mapping) upsertRecord(ctx context.Context, logger *slog.Logger, data route53RequestData) error {
	if err := route53Request(ctx, types.ChangeActionUpsert, data, m.client, logger); err != nil {
		return err
	}
	m.records = append(m.records, data)
	return nil
}

// Cleanup deletes every Route 53 record this mapping created. It is safe to call more than once.
func (m *Mapping) Cleanup(ctx context.Context, logger *slog.Logger) error {
	if m == nil {
		return nil
	}

	var cleanupErr error
	m.once.Do(func() {
		for _, data := range m.records {
			err := route53Request(ctx, types.ChangeActionDelete, data, m.client, logger)
			if err != nil {
				logger.ErrorContext(ctx, "error deleting route53 record", "name", data.recordName, "hostedZoneId", data.hostedZoneId, "err", err)
				if cleanupErr == nil {
					cleanupErr = err
				}
			}
		}
	})
	return cleanupErr
}
