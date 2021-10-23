#!/bin/bash

IMAGE_REPO=$1

IMAGE_NAME="gpu-access-controller"

NAME_SPACE="admission-webhook-ns"

docker build -t $IMAGE_REPO/$IMAGE_NAME:latest -f webhook-server/Dockerfile webhook-server
docker push $IMAGE_REPO/$IMAGE_NAME:latest

kubectl create ns $NAME_SPACE

shell-scripts/webhook-create-signed-cert.sh --service gpu-access-controller-webhook-svc --secret gpu-access-controller-webhook-certs --namespace $NAME_SPACE

cat manifests/mutatingwebhook.yaml | shell-scripts/webhook-patch-ca-bundle.sh > manifests/mutatingwebhook-ca-bundle.yaml

envsubst < manifests/deployment.yaml.template > manifests/deployment.yaml

kubectl apply -f manifests/nginxconfigmap.yaml
kubectl apply -f manifests/configmap.yaml
kubectl apply -f manifests/service.yaml
kubectl apply -f manifests/mutatingwebhook-ca-bundle.yaml
kubectl apply -f manifests/deployment.yaml