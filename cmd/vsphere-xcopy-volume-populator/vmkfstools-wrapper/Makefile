SHELL := /bin/bash

test:
	python vmkfstools_wrapper_test.py

build: test
	./create-vib.sh

install: build
	ANSIBLE_HOST_KEY_CHECKING=false \
	VMWARE_HOST=$${VMWARE_HOST:-$(shell ansible-vault view ~/vaults/vmware_vault.yaml --vault-password-file ~/vaults/passfile |yq .hostname)} \
	VMWARE_USER=$${VMWARE_USER:-$(shell ansible-vault view ~/vaults/vmware_vault.yaml --vault-password-file ~/vaults/passfile |yq .username)} \
	VMWARE_PASSWORD=$${VMWARE_PASSWORD:-$(shell ansible-vault view ~/vaults/vmware_vault.yaml --vault-password-file ~/vaults/passfile |yq .password)} \
	ansible-playbook vib-install-playbook.yaml -i esxi_hosts.yaml

inventory: 
	VMWARE_HOST=$${VMWARE_HOST:-$(shell ansible-vault view ~/vaults/vmware_vault.yaml --vault-password-file ~/vaults/passfile |yq .hostname)} \
	VMWARE_USER=$${VMWARE_USER:-$(shell ansible-vault view ~/vaults/vmware_vault.yaml --vault-password-file ~/vaults/passfile |yq .username)} \
	VMWARE_PASSWORD=$${VMWARE_PASSWORD:-$(shell ansible-vault view ~/vaults/vmware_vault.yaml --vault-password-file ~/vaults/passfile |yq .password)} \
	ansible-inventory --list -i esxi_hosts.yaml  -vvvv --become

