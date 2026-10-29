package provider

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func TestRcloneNativeSDKRecordsActualPutBoundary(t *testing.T) {
	profile := validRcloneNativeProfileForTest()
	request := RcloneNativeControlWriteRequest{PhysicalKey: profile.ManagedPrefix + "control/commit.json", Payload: []byte("control"), MaxBytes: 1024, EncryptionProfile: RcloneNativeSSES3V1}
	client := &rcloneNativeS3ClientFake{}
	adapter := newRcloneNativeS3SDK(client, "123456789012", profile, nil)
	invalid := request
	invalid.PhysicalKey = "outside-managed-root"
	_, err := adapter.PutControlVersion(context.Background(), invalid)
	var fact *rcloneMutationCommandError
	if !errors.As(err, &fact) || fact.attempted || !fact.quiesced || client.putInput != nil {
		t.Fatalf("local rejection did not retain pre-call fact: %v", err)
	}
	cause := errors.New("controlled SDK failure")
	client.putError = cause
	_, err = adapter.PutControlVersion(context.Background(), request)
	fact = nil
	if !errors.As(err, &fact) || !fact.attempted || fact.quiesced || !errors.Is(err, cause) || client.putInput == nil {
		t.Fatalf("called SDK failure was not unknown: %v", err)
	}
	client.putError = nil
	client.putOutput = &s3.PutObjectOutput{}
	_, err = adapter.PutControlVersion(context.Background(), request)
	fact = nil
	if !errors.As(err, &fact) || !fact.attempted || fact.quiesced {
		t.Fatalf("missing VersionID proved completion: %v", err)
	}
	client.putOutput = &s3.PutObjectOutput{VersionId: aws.String("confirmed-version")}
	result, err := adapter.PutControlVersion(context.Background(), request)
	if err != nil || result.VersionID != "confirmed-version" {
		t.Fatalf("valid VersionID was not accepted: %+v %v", result, err)
	}
}
