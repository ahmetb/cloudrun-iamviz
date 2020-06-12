#!/usr/bin/env bash
set -euo pipefail

region="${REGION:=us-central1}"

function create_svc_account {
  local proj name email
  proj="$(gcloud config get-value core/project -q)"
  if [[ -z "$proj" ]]; then
    echo >&2 "could not determine project from gcloud"
    exit 1
  fi


  name="${1:-name not specified}"
  email="${name}@${proj}.iam.gserviceaccount.com"

  if ! gcloud iam service-accounts describe "$email" 1>/dev/null 2>&1; then
    gcloud iam service-accounts create "$name" 1>/dev/null
    echo "$(tput setaf 2)Created SA ${email}.$(tput sgr0)"
  else
    echo >&2 "SA ${email} already exists."
  fi
  echo "$email"
}

function create_cloud_run_service {
  name="${1:-name not specified}"
  svc_account="${2:-service account not specified}"

  gcloud run deploy "$name" \
    --platform=managed \
    --region="$region" \
    --image=gcr.io/cloudrun/hello \
    --service-account "${svc_account}" -q 1>/dev/null

  echo "$(tput setaf 2)Created service ${name}$(tput sgr0)"
}

function give_permission {
  svc_account="${1:-service account not specified}"
  svc="${2:-service not specified}"

  gcloud run services add-iam-policy-binding \
    "$svc" --platform=managed --region="${region}" \
    --member="serviceAccount:${svc_account}" \
    --role=roles/run.invoker 1>/dev/null

  echo "$(tput setaf 2)Added ${svc_account} binding to ${svc}$(tput sgr0)"
}

acct1=$(create_svc_account "test-acct-1")
acct2=$(create_svc_account "test-acct-2")
acct3=$(create_svc_account "test-acct-3")
acct4=$(create_svc_account "test-acct-4")
acct5=$(create_svc_account "test-acct-5")

create_cloud_run_service "svc-a" "$acct1"
create_cloud_run_service "svc-b" "$acct2"
create_cloud_run_service "svc-c" "$acct3"
create_cloud_run_service "svc-d" "$acct4"
create_cloud_run_service "svc-e" "$acct5"

give_permission "$acct1" "svc-c"
give_permission "$acct1" "svc-d"
give_permission "$acct2" "svc-c"
give_permission "$acct2" "svc-e"
give_permission "$acct5" "svc-a"

