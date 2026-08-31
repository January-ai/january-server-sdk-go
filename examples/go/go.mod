module january-server-go-example

go 1.26

require (
	github.com/January-ai/january-server-sdk-go v0.0.0
	github.com/joho/godotenv v1.5.1
)

require golang.org/x/image v0.45.0 // indirect

replace github.com/January-ai/january-server-sdk-go => ../..
