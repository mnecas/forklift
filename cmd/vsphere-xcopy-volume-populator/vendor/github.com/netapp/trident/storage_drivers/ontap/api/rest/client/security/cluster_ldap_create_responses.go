// Code generated by go-swagger; DO NOT EDIT.

package security

// This file was generated by the swagger tool.
// Editing this file might prove futile when you re-run the swagger generate command

import (
	"fmt"
	"io"

	"github.com/go-openapi/runtime"
	"github.com/go-openapi/strfmt"

	"github.com/netapp/trident/storage_drivers/ontap/api/rest/models"
)

// ClusterLdapCreateReader is a Reader for the ClusterLdapCreate structure.
type ClusterLdapCreateReader struct {
	formats strfmt.Registry
}

// ReadResponse reads a server response into the received o.
func (o *ClusterLdapCreateReader) ReadResponse(response runtime.ClientResponse, consumer runtime.Consumer) (interface{}, error) {
	switch response.Code() {
	case 201:
		result := NewClusterLdapCreateCreated()
		if err := result.readResponse(response, consumer, o.formats); err != nil {
			return nil, err
		}
		return result, nil
	default:
		result := NewClusterLdapCreateDefault(response.Code())
		if err := result.readResponse(response, consumer, o.formats); err != nil {
			return nil, err
		}
		if response.Code()/100 == 2 {
			return result, nil
		}
		return nil, result
	}
}

// NewClusterLdapCreateCreated creates a ClusterLdapCreateCreated with default headers values
func NewClusterLdapCreateCreated() *ClusterLdapCreateCreated {
	return &ClusterLdapCreateCreated{}
}

/*
ClusterLdapCreateCreated describes a response with status code 201, with default header values.

Created
*/
type ClusterLdapCreateCreated struct {

	/* Useful for tracking the resource location
	 */
	Location string

	Payload *models.LdapServiceResponse
}

// IsSuccess returns true when this cluster ldap create created response has a 2xx status code
func (o *ClusterLdapCreateCreated) IsSuccess() bool {
	return true
}

// IsRedirect returns true when this cluster ldap create created response has a 3xx status code
func (o *ClusterLdapCreateCreated) IsRedirect() bool {
	return false
}

// IsClientError returns true when this cluster ldap create created response has a 4xx status code
func (o *ClusterLdapCreateCreated) IsClientError() bool {
	return false
}

// IsServerError returns true when this cluster ldap create created response has a 5xx status code
func (o *ClusterLdapCreateCreated) IsServerError() bool {
	return false
}

// IsCode returns true when this cluster ldap create created response a status code equal to that given
func (o *ClusterLdapCreateCreated) IsCode(code int) bool {
	return code == 201
}

func (o *ClusterLdapCreateCreated) Error() string {
	return fmt.Sprintf("[POST /security/authentication/cluster/ldap][%d] clusterLdapCreateCreated  %+v", 201, o.Payload)
}

func (o *ClusterLdapCreateCreated) String() string {
	return fmt.Sprintf("[POST /security/authentication/cluster/ldap][%d] clusterLdapCreateCreated  %+v", 201, o.Payload)
}

func (o *ClusterLdapCreateCreated) GetPayload() *models.LdapServiceResponse {
	return o.Payload
}

func (o *ClusterLdapCreateCreated) readResponse(response runtime.ClientResponse, consumer runtime.Consumer, formats strfmt.Registry) error {

	// hydrates response header Location
	hdrLocation := response.GetHeader("Location")

	if hdrLocation != "" {
		o.Location = hdrLocation
	}

	o.Payload = new(models.LdapServiceResponse)

	// response payload
	if err := consumer.Consume(response.Body(), o.Payload); err != nil && err != io.EOF {
		return err
	}

	return nil
}

// NewClusterLdapCreateDefault creates a ClusterLdapCreateDefault with default headers values
func NewClusterLdapCreateDefault(code int) *ClusterLdapCreateDefault {
	return &ClusterLdapCreateDefault{
		_statusCode: code,
	}
}

/*
	ClusterLdapCreateDefault describes a response with status code -1, with default header values.

	ONTAP Error Response Codes

| Error Code | Description |
| ---------- | ----------- |
| 4915203    | The specified LDAP schema does not exist. |
| 4915207    | The specified LDAP servers contain duplicate server entries. |
| 4915229    | DNS resolution failed due to an internal error. Contact technical support if this issue persists. |
| 4915231    | DNS resolution failed for one or more of the specified LDAP servers. Verify that a valid DNS server is configured. |
| 23724132   | DNS resolution failed for all the specified LDAP servers. Verify that a valid DNS server is configured. |
| 4915234    | The specified LDAP server is not supported because it is one of the following: multicast, loopback, 0.0.0.0, or broadcast. |
| 4915248    | LDAP servers cannot be empty or "-". Specified FQDN is invalid because it is empty or "-" or it contains either special characters or "-" at the start or end of the domain.  |
| 4915251    | STARTTLS and LDAPS cannot be used together. |
| 4915257    | The LDAP configuration is invalid. Verify that bind-dn and bind password are correct. |
| 4915258    | The LDAP configuration is invalid. Verify that the servers are reachable and that the network configuration is correct. |
| 13434916   | The SVM is in the process of being created. Wait a few minutes, and then try the command again. |
| 23724130   | Cannot use an IPv6 name server address because there are no IPv6 interfaces. |
| 4915252    | LDAP referral is not supported with STARTTLS, with session security levels sign, seal or with LDAPS. |
*/
type ClusterLdapCreateDefault struct {
	_statusCode int

	Payload *models.ErrorResponse
}

// Code gets the status code for the cluster ldap create default response
func (o *ClusterLdapCreateDefault) Code() int {
	return o._statusCode
}

// IsSuccess returns true when this cluster ldap create default response has a 2xx status code
func (o *ClusterLdapCreateDefault) IsSuccess() bool {
	return o._statusCode/100 == 2
}

// IsRedirect returns true when this cluster ldap create default response has a 3xx status code
func (o *ClusterLdapCreateDefault) IsRedirect() bool {
	return o._statusCode/100 == 3
}

// IsClientError returns true when this cluster ldap create default response has a 4xx status code
func (o *ClusterLdapCreateDefault) IsClientError() bool {
	return o._statusCode/100 == 4
}

// IsServerError returns true when this cluster ldap create default response has a 5xx status code
func (o *ClusterLdapCreateDefault) IsServerError() bool {
	return o._statusCode/100 == 5
}

// IsCode returns true when this cluster ldap create default response a status code equal to that given
func (o *ClusterLdapCreateDefault) IsCode(code int) bool {
	return o._statusCode == code
}

func (o *ClusterLdapCreateDefault) Error() string {
	return fmt.Sprintf("[POST /security/authentication/cluster/ldap][%d] cluster_ldap_create default  %+v", o._statusCode, o.Payload)
}

func (o *ClusterLdapCreateDefault) String() string {
	return fmt.Sprintf("[POST /security/authentication/cluster/ldap][%d] cluster_ldap_create default  %+v", o._statusCode, o.Payload)
}

func (o *ClusterLdapCreateDefault) GetPayload() *models.ErrorResponse {
	return o.Payload
}

func (o *ClusterLdapCreateDefault) readResponse(response runtime.ClientResponse, consumer runtime.Consumer, formats strfmt.Registry) error {

	o.Payload = new(models.ErrorResponse)

	// response payload
	if err := consumer.Consume(response.Body(), o.Payload); err != nil && err != io.EOF {
		return err
	}

	return nil
}
