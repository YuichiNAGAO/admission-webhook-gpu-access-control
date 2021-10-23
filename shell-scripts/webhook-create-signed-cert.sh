#!/bin/bash

set -e

while [[ $# -gt 0 ]]; do
    case ${1} in
        --service)
            service="$2"
            shift
            ;;
        --secret)
            secret="$2"
            shift
            ;;
        --namespace)
            namespace="$2"
            shift
            ;;
        *)
            exit 1
            ;;
    esac
    shift
done

[ -z "${service}" ] && service=gpu-access-controller-webhook-svc
[ -z "${secret}" ] && secret=gpu-access-controller-webhook-certs
[ -z "${namespace}" ] && namespace=admission-webhook-ns

if [ ! -x "$(command -v openssl)" ]; then
    echo "openssl not found"
    exit 1
fi

csrName=${service}.${namespace}
tmpdir=$(mktemp -d)
mkdir -p $tmpdir
echo "creating certs in tmpdir ${tmpdir} "

# create csr.conf
cat <<EOF >> "${tmpdir}"/csr.conf
[req]
req_extensions = v3_req
distinguished_name = req_distinguished_name
[req_distinguished_name]
[ v3_req ]
basicConstraints = CA:FALSE
keyUsage = nonRepudiation, digitalSignature, keyEncipherment
extendedKeyUsage = serverAuth
subjectAltName = @alt_names
[alt_names]
DNS.1 = ${service}
DNS.2 = ${service}.${namespace}
DNS.3 = ${service}.${namespace}.svc
EOF

# create private key
openssl genrsa -out "${tmpdir}"/server-key.pem 2048
# create csr(certificate signing request)
openssl req -new -key "${tmpdir}"/server-key.pem -subj "/CN=${service}.${namespace}.svc" -out "${tmpdir}"/server.csr -config "${tmpdir}"/csr.conf

# clean-up any previously created CSR for our service. Ignore errors if not present.
kubectl delete csr ${csrName} 2>/dev/null || true

# create  server cert/key CSR and  send to k8s API
cat <<EOF | kubectl create -f -
apiVersion: certificates.k8s.io/v1beta1
kind: CertificateSigningRequest
metadata:
  name: ${csrName}
spec:
  groups:
  - system:authenticated
  request: $(< "${tmpdir}"/server.csr base64 | tr -d '\n')
  usages:
  - digital signature
  - key encipherment
  - server auth
EOF

# verify CSR has been created
while true; do
    if kubectl get csr ${csrName}; then
        break
    else
        sleep 1
    fi
done

# approve and fetch the signed certificate
kubectl certificate approve ${csrName}
# verify certificate has been signed
for _ in $(seq 10); do
    serverCert=$(kubectl get csr ${csrName} -o jsonpath='{.status.certificate}')
    if [[ ${serverCert} != '' ]]; then
        break
    fi
    sleep 1
done
if [[ ${serverCert} == '' ]]; then
    echo "ERROR: After approving csr ${csrName}, the signed certificate did not appear on the resource. Giving up after 10 attempts." >&2
    exit 1
fi
# encode server certificate
echo "${serverCert}" | openssl base64 -d -A -out "${tmpdir}"/server-cert.pem


# create the secret with CA cert and server cert/key
kubectl create secret generic ${secret} \
        --from-file=key.pem="${tmpdir}"/server-key.pem \
        --from-file=cert.pem="${tmpdir}"/server-cert.pem \
        --dry-run -o yaml |
    kubectl -n ${namespace} apply -f -


rm -rf $tmpdir
echo "removed ${tmpdir}"
