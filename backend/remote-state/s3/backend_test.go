package s3

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/dynamodb"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/hashicorp/terraform/backend"
)

// verify that we are doing ACC tests or the S3 tests specifically
func testACC(t *testing.T) {
	skip := os.Getenv("TF_ACC") == "" && os.Getenv("TF_S3_TEST") == ""
	if skip {
		t.Log("s3 backend tests require setting TF_ACC or TF_S3_TEST")
		t.Skip()
	}
	if os.Getenv("AWS_DEFAULT_REGION") == "" {
		os.Setenv("AWS_DEFAULT_REGION", "us-west-2")
	}
}

func TestBackend_impl(t *testing.T) {
	var _ backend.Backend = new(Backend)
}

func TestBackendConfig(t *testing.T) {
	// This test just instantiates the client. Shouldn't make any actual
	// requests nor incur any costs.

	config := map[string]interface{}{
		"region":     "us-west-1",
		"bucket":     "tf-test",
		"key":        "state",
		"encrypt":    true,
		"access_key": "ACCESS_KEY",
		"secret_key": "SECRET_KEY",
		"lock_table": "dynamoTable",
	}

	b := backend.TestBackendConfig(t, New(), config).(*Backend)

	if *b.client.nativeClient.Config.Region != "us-west-1" {
		t.Fatalf("Incorrect region was populated")
	}
	if b.client.bucketName != "tf-test" {
		t.Fatalf("Incorrect bucketName was populated")
	}
	if b.client.keyName != "state" {
		t.Fatalf("Incorrect keyName was populated")
	}

	credentials, err := b.client.nativeClient.Config.Credentials.Get()
	if err != nil {
		t.Fatalf("Error when requesting credentials")
	}
	if credentials.AccessKeyID != "ACCESS_KEY" {
		t.Fatalf("Incorrect Access Key Id was populated")
	}
	if credentials.SecretAccessKey != "SECRET_KEY" {
		t.Fatalf("Incorrect Secret Access Key was populated")
	}
}

func TestBackend(t *testing.T) {
	testACC(t)

	bucketName := fmt.Sprintf("terraform-remote-s3-test-%x", time.Now().Unix())
	keyName := "testState"

	b := backend.TestBackendConfig(t, New(), map[string]interface{}{
		"bucket":  bucketName,
		"key":     keyName,
		"encrypt": true,
	}).(*Backend)

	createS3Bucket(t, b.client, bucketName)
	defer deleteS3Bucket(t, b.client, bucketName)

	backend.TestBackend(t, b, nil)
}

func TestBackendLocked(t *testing.T) {
	testACC(t)

	bucketName := fmt.Sprintf("terraform-remote-s3-test-%x", time.Now().Unix())
	keyName := "testState"

	b1 := backend.TestBackendConfig(t, New(), map[string]interface{}{
		"bucket":     bucketName,
		"key":        keyName,
		"encrypt":    true,
		"lock_table": bucketName,
	}).(*Backend)

	b2 := backend.TestBackendConfig(t, New(), map[string]interface{}{
		"bucket":     bucketName,
		"key":        keyName,
		"encrypt":    true,
		"lock_table": bucketName,
	}).(*Backend)

	createS3Bucket(t, b1.client, bucketName)
	defer deleteS3Bucket(t, b1.client, bucketName)
	createDynamoDBTable(t, b1.client, bucketName)
	defer deleteDynamoDBTable(t, b1.client, bucketName)

	backend.TestBackend(t, b1, b2)
}

func createS3Bucket(t *testing.T, c *S3Client, bucketName string) {
	createBucketReq := &s3.CreateBucketInput{
		Bucket: &bucketName,
	}

	// Be clear about what we're doing in case the user needs to clean
	// this up later.
	t.Logf("creating S3 bucket %s in %s", bucketName, *c.nativeClient.Config.Region)
	_, err := c.nativeClient.CreateBucket(createBucketReq)
	if err != nil {
		t.Fatal("failed to create test S3 bucket:", err)
	}
}

func deleteS3Bucket(t *testing.T, c *S3Client, bucketName string) {
	deleteBucketReq := &s3.DeleteBucketInput{
		Bucket: &bucketName,
	}

	_, err := c.nativeClient.DeleteBucket(deleteBucketReq)
	if err != nil {
		t.Logf("WARNING: Failed to delete the test S3 bucket. It may have been left in your AWS account and may incur storage charges. (error was %s)", err)
	}
}

// create the dynamoDB table, and wait until we can query it.
func createDynamoDBTable(t *testing.T, c *S3Client, tableName string) {
	createInput := &dynamodb.CreateTableInput{
		AttributeDefinitions: []*dynamodb.AttributeDefinition{
			{
				AttributeName: aws.String("LockID"),
				AttributeType: aws.String("S"),
			},
		},
		KeySchema: []*dynamodb.KeySchemaElement{
			{
				AttributeName: aws.String("LockID"),
				KeyType:       aws.String("HASH"),
			},
		},
		ProvisionedThroughput: &dynamodb.ProvisionedThroughput{
			ReadCapacityUnits:  aws.Int64(5),
			WriteCapacityUnits: aws.Int64(5),
		},
		TableName: aws.String(tableName),
	}

	_, err := c.dynClient.CreateTable(createInput)
	if err != nil {
		t.Fatal(err)
	}

	// now wait until it's ACTIVE
	start := time.Now()
	time.Sleep(time.Second)

	describeInput := &dynamodb.DescribeTableInput{
		TableName: aws.String(tableName),
	}

	for {
		resp, err := c.dynClient.DescribeTable(describeInput)
		if err != nil {
			t.Fatal(err)
		}

		if *resp.Table.TableStatus == "ACTIVE" {
			return
		}

		if time.Since(start) > time.Minute {
			t.Fatalf("timed out creating DynamoDB table %s", tableName)
		}

		time.Sleep(3 * time.Second)
	}

}

func deleteDynamoDBTable(t *testing.T, c *S3Client, tableName string) {
	params := &dynamodb.DeleteTableInput{
		TableName: aws.String(tableName),
	}
	_, err := c.dynClient.DeleteTable(params)
	if err != nil {
		t.Logf("WARNING: Failed to delete the test DynamoDB table %q. It has been left in your AWS account and may incur charges. (error was %s)", tableName, err)
	}
}
