// Copyright (C) 2026 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package nrfclient_test

import (
	"context"

	"github.com/omec-project/openapi/v2/models"
	"github.com/omec-project/openapi/v2/nrfclient"
	"github.com/omec-project/openapi/v2/nrfregistration"
)

// An NF keeps its own profile builder and its own record of the NF instance ID, and plugs the
// client into the registration service.
func Example_withNrfregistration() {
	// The NF chooses its instance ID once, at startup, and keeps it: it is the path of every request about
	// the NF (PUT, PATCH and DELETE /nf-instances/{nfInstanceID}), so the first registration needs it as well.
	// A UUID, as uuid.NewString() makes. Register reports another one if the NRF assigned it.
	nfInstanceID := "3fa85f64-5717-4562-b3fc-2c963f66afa6"
	buildProfile := func(plmns []models.PlmnId) models.NFProfile {
		p := models.NewNFProfileWithDefaults()
		p.SetNfInstanceId(nfInstanceID)
		p.SetNfType(models.NFTYPE_UDM)
		p.SetNfStatus(models.NFSTATUS_REGISTERED) // nfInstanceId, nfType and nfStatus are required
		p.SetPlmnList(plmns)
		return *p
	}
	client := nrfclient.New("http://nrf:8000")

	svc, err := nrfregistration.New(nrfregistration.Config[[]models.PlmnId]{
		NfName:  "UDM",
		IsEmpty: func(c []models.PlmnId) bool { return len(c) == 0 },
		Register: func(ctx context.Context, plmns []models.PlmnId) (*models.NFProfile, error) {
			res, err := client.Register(ctx, buildProfile(plmns))
			if err != nil {
				return nil, err
			}
			if res.NfInstanceID != "" {
				nfInstanceID = res.NfInstanceID
			}
			return res.Profile, nil
		},
		Update: func(ctx context.Context, patch []models.PatchItem) (*models.NFProfile, *models.ProblemDetails, error) {
			return client.Update(ctx, nfInstanceID, patch)
		},
		Deregister: func(ctx context.Context) error {
			return client.Deregister(ctx, nfInstanceID)
		},
	})
	if err != nil {
		panic(err)
	}
	_ = svc
}
