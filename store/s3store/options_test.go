package s3store

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// With no endpoint and no path style the options change nothing about the
// client, so a deployment on AWS gets exactly the client it always had. With
// an endpoint the client goes there, and stops adding the checksum AWS
// understands and other stores do not.
func TestConfigureChangesNothingUnlessAsked(t *testing.T) {
	var c s3.Options
	configure(Options{Bucket: "b"})(&c)
	if c.BaseEndpoint != nil || c.UsePathStyle || c.RequestChecksumCalculation != 0 || c.ResponseChecksumValidation != 0 {
		t.Fatalf("an AWS deployment's client was changed: %+v", c)
	}

	c = s3.Options{}
	configure(Options{Bucket: "b", Endpoint: "https://s3.example.test", PathStyle: true})(&c)
	if got := aws.ToString(c.BaseEndpoint); got != "https://s3.example.test" {
		t.Fatalf("endpoint = %q", got)
	}
	if !c.UsePathStyle {
		t.Fatal("path style was asked for and not set")
	}
	if c.RequestChecksumCalculation != aws.RequestChecksumCalculationWhenRequired {
		t.Fatal("a store that is not AWS would be sent a CRC32 it may not understand")
	}
	if c.ResponseChecksumValidation != aws.ResponseChecksumValidationWhenRequired {
		t.Fatal("a store that is not AWS would be asked for a checksum it may not send")
	}

	// Path style on its own, for a store at the AWS endpoint whose
	// certificate does not cover a bucket subdomain.
	c = s3.Options{}
	configure(Options{Bucket: "b", PathStyle: true})(&c)
	if !c.UsePathStyle || c.BaseEndpoint != nil {
		t.Fatalf("path style alone changed more than path style: %+v", c)
	}
}
