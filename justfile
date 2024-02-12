build-protos:
    protoc  --go_out=./grpc/suave \
            --go_opt=paths=source_relative \
            proto/suave/bundle.proto
