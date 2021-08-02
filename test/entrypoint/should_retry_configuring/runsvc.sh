#!/bin/bash

set -euo pipefail

export LIGHTGREEN='\e[0;32m'
export LIGHTRED='\e[0;31m'
export WHITE='\e[0;97m'
export RESET='\e[0m'

log(){
  printf "\t${WHITE}$@${RESET}\n" 2>&1
}

success(){
  printf "\t${LIGHTGREEN}$@${RESET}\n" 2>&1
}

error(){
  printf "\t${LIGHTRED}$@${RESET}\n" 2>&1
  exit 1
}

success ""
success "Running the service..."
# SHOULD NOT HAPPEN
# creating a file to show this script has run
touch runsvc_ran
success "...successful"
success ""


