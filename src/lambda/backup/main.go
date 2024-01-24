package main

import (
	"context"
	"github.com/aws/aws-lambda-go/lambda"
)

func main() {
	lambda.StartWithOptions(
		func(ctx context.Context) error {

			return nil
		},
	)
}
