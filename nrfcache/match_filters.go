// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2022 Infosys Limited
// SPDX-FileCopyrightText: 2024 Canonical Ltd.
/*
 *  Match the NF profiles based on the parameters
 */

// This file contains apis to match the nf profiles based on the parameters provided in the
// Nnrf_NFDiscovery.ApiSearchNFInstancesRequest. There is a match function provided for each NF type
// which must be updated with logic to compare profiles based on the applicable params in
// Nnrf_NFDiscovery.ApiSearchNFInstancesRequest

package nrfcache

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/omec-project/openapi/v2/Nnrf_NFDiscovery"
	"github.com/omec-project/openapi/v2/logger"
	"github.com/omec-project/openapi/v2/models"
)

type MatchFilter func(profile *models.NFProfileDiscovery, opts Nnrf_NFDiscovery.ApiSearchNFInstancesRequest) (bool, error)

type MatchFilters map[models.NFType]MatchFilter

var matchFilters = MatchFilters{
	models.NFTYPE_SMF:  MatchSmfProfile,
	models.NFTYPE_AUSF: MatchAusfProfile,
	models.NFTYPE_PCF:  MatchPcfProfile,
	models.NFTYPE_NSSF: MatchNssfProfile,
	models.NFTYPE_UDM:  MatchUdmProfile,
	models.NFTYPE_AMF:  MatchAmfProfile,
}

func MatchSmfProfile(profile *models.NFProfileDiscovery, opts Nnrf_NFDiscovery.ApiSearchNFInstancesRequest) (bool, error) {
	serviceNames := opts.GetServiceNames()
	if serviceNames != nil && len(*serviceNames) > 0 {
		found := false
		for _, requiredService := range *serviceNames {
			for _, nfService := range profile.NfServices {
				if nfService.ServiceName == requiredService {
					found = true
					break
				}
			}
			if found {
				break
			}
		}

		if !found {
			logger.NrfcacheLog.Debugf("smf match failed: no service match for %s", profile.NfInstanceId)
			return false, nil
		}
	}

	snssais := opts.GetSnssais()
	if snssais != nil {
		matchCount := 0
		for _, reqSnssai := range *snssais {
			// Snssai in the smfInfo has priority
			if profile.SmfInfo != nil && profile.SmfInfo.SNssaiSmfInfoList != nil {
				for _, s := range profile.SmfInfo.SNssaiSmfInfoList {
					if (s.SNssai.GetSst() == reqSnssai.GetSst()) && (s.SNssai.GetSd() == reqSnssai.GetSd()) {
						matchCount++
					}
				}
			} else if profile.AllowedNssais != nil {
				for _, s := range profile.AllowedNssais {
					if (s.GetSst() == reqSnssai.GetSst()) && (s.GetSd() == reqSnssai.GetSd()) {
						matchCount++
					}
				}
			}
		}

		// if at least one matching snssai has been found
		if matchCount == 0 {
			return false, nil
		}
	}

	// validate dnn
	dnn := opts.GetDnn()
	if dnn != nil {
		// if a dnn is provided by the upper layer, check for the exact match
		// or wild card match
		dnnMatched := false

		if profile.SmfInfo != nil && profile.SmfInfo.SNssaiSmfInfoList != nil {
		matchDnnLoop:
			for _, s := range profile.SmfInfo.SNssaiSmfInfoList {
				if s.DnnSmfInfoList != nil {
					for _, d := range s.DnnSmfInfoList {
						if d.GetDnn() == *dnn || d.GetDnn() == "*" {
							dnnMatched = true
							break matchDnnLoop
						}
					}
				}
			}
		}

		if !dnnMatched {
			return false, nil
		}
	}
	logger.NrfcacheLog.Infof("smf match found, nfInstance Id %v", profile.NfInstanceId)
	return true, nil
}

func matchSupiRange(supi string, supiRange []models.SupiRange) bool {
	for _, s := range supiRange {
		if matchSingleSupiRange(supi, s) {
			return true
		}
	}
	return false
}

func matchSingleSupiRange(supi string, supiRange models.SupiRange) bool {
	// Handle regex pattern matching (preferred method)
	if pattern := supiRange.GetPattern(); pattern != "" {
		r, err := regexp.Compile(pattern)
		if err != nil {
			logger.NrfcacheLog.Errorf("invalid SUPI pattern '%s': %v", pattern, err)
			return false
		}
		return r.MatchString(supi)
	}

	// Handle numeric range
	start := supiRange.GetStart()
	end := supiRange.GetEnd()

	if start == "" || end == "" {
		return false
	}

	return isSupiInNumericRange(supi, start, end)
}

func isSupiInNumericRange(supi, start, end string) bool {
	supiNum := extractSupiNumber(supi)
	startNum := extractSupiNumber(start)
	endNum := extractSupiNumber(end)

	// If extraction failed for any, fall back to string comparison
	if supiNum == "" || startNum == "" || endNum == "" {
		return start <= supi && supi <= end
	}

	// For same-length numbers, string comparison works
	if len(supiNum) == len(startNum) && len(startNum) == len(endNum) {
		return startNum <= supiNum && supiNum <= endNum
	}

	// Different lengths require numeric comparison
	supiInt, err1 := strconv.ParseInt(supiNum, 10, 64)
	startInt, err2 := strconv.ParseInt(startNum, 10, 64)
	endInt, err3 := strconv.ParseInt(endNum, 10, 64)

	if err1 != nil || err2 != nil || err3 != nil {
		// Fallback to string comparison
		return start <= supi && supi <= end
	}

	return startInt <= supiInt && supiInt <= endInt
}

func extractSupiNumber(supi string) string {
	if strings.HasPrefix(supi, "imsi-") {
		return supi[5:]
	}
	if strings.HasPrefix(supi, "nai-") {
		return supi[4:]
	}
	// Return as-is if no known prefix
	return supi
}

func MatchAusfProfile(profile *models.NFProfileDiscovery, opts Nnrf_NFDiscovery.ApiSearchNFInstancesRequest) (bool, error) {
	supi := opts.GetSupi()
	if supi != nil {
		if profile.AusfInfo == nil || len(profile.AusfInfo.SupiRanges) == 0 {
			logger.NrfcacheLog.Debugf("ausf match failed: no SUPI ranges for %s", profile.NfInstanceId)
			return false, nil
		}

		matchFound := matchSupiRange(*supi, profile.AusfInfo.SupiRanges)
		if matchFound {
			logger.NrfcacheLog.Debugf("ausf match successful for %s", profile.NfInstanceId)
		} else {
			logger.NrfcacheLog.Debugf("ausf match failed: SUPI range mismatch for %s", profile.NfInstanceId)
		}
		return matchFound, nil
	}

	logger.NrfcacheLog.Debugf("ausf match successful (no SUPI filter) for %s", profile.NfInstanceId)
	return true, nil
}

func MatchNssfProfile(profile *models.NFProfileDiscovery, opts Nnrf_NFDiscovery.ApiSearchNFInstancesRequest) (bool, error) {
	logger.NrfcacheLog.Infoln("nssf match found")
	return true, nil
}

func MatchAmfProfile(profile *models.NFProfileDiscovery, opts Nnrf_NFDiscovery.ApiSearchNFInstancesRequest) (bool, error) {
	if profile == nil {
		return false, fmt.Errorf("profile cannot be nil")
	}

	if profile.NfType != models.NFTYPE_AMF {
		return false, fmt.Errorf("profile is not AMF type: %v", profile.NfType)
	}
	targetPlmnList := opts.GetTargetPlmnList()
	if targetPlmnList != nil && len(*targetPlmnList) > 0 {
		profilePlmnList := profile.GetPlmnList()
		if len(profilePlmnList) == 0 {
			logger.NrfcacheLog.Debugf("amf match failed: no profile PLMNs for %s", profile.NfInstanceId)
			return false, nil
		}

		found := false
		for _, targetPlmn := range *targetPlmnList {
			if slices.Contains(profilePlmnList, targetPlmn) {
				found = true
				break
			}
		}

		if !found {
			logger.NrfcacheLog.Debugf("amf match failed: no PLMN match for %s", profile.NfInstanceId)
			return false, nil
		}
	}

	targetNfInstanceId := opts.GetTargetNfInstanceId()
	if targetNfInstanceId != nil && profile.GetNfInstanceId() != *targetNfInstanceId {
		logger.NrfcacheLog.Debugf("amf match failed: NF instance ID mismatch for %s", profile.NfInstanceId)
		return false, nil
	}

	if profile.AmfInfo != nil {
		guamiOpt := opts.GetGuami()
		if guamiOpt != nil && (profile.AmfInfo.GuamiList == nil || !slices.Contains(profile.AmfInfo.GuamiList, *guamiOpt)) {
			logger.NrfcacheLog.Debugf("amf match failed: GUAMI mismatch for %s", profile.NfInstanceId)
			return false, nil
		}

		amfRegionId := opts.GetAmfRegionId()
		if amfRegionId != nil && profile.AmfInfo.GetAmfRegionId() != *amfRegionId {
			logger.NrfcacheLog.Debugf("amf match failed: AMF region ID mismatch for %s", profile.NfInstanceId)
			return false, nil
		}

		amfSetId := opts.GetAmfSetId()
		if amfSetId != nil && profile.AmfInfo.GetAmfSetId() != *amfSetId {
			logger.NrfcacheLog.Debugf("amf match failed: AMF set ID mismatch for %s", profile.NfInstanceId)
			return false, nil
		}
	} else {
		// Handle case where AMF-specific filters are provided but AmfInfo is nil
		if opts.GetGuami() != nil || opts.GetAmfRegionId() != nil || opts.GetAmfSetId() != nil {
			logger.NrfcacheLog.Debugf("amf match failed: AMF filters provided but no AmfInfo for %s", profile.NfInstanceId)
			return false, nil
		}
	}

	logger.NrfcacheLog.Infof("amf match found = %v", profile.NfInstanceId)
	return true, nil
}

func MatchPcfProfile(profile *models.NFProfileDiscovery, opts Nnrf_NFDiscovery.ApiSearchNFInstancesRequest) (bool, error) {
	supi := opts.GetSupi()
	if supi != nil {
		if profile.PcfInfo == nil || len(profile.PcfInfo.SupiRanges) == 0 {
			logger.NrfcacheLog.Infof("pcf match found = false (no SUPI ranges)")
			return false, nil
		}

		matchFound := matchSupiRange(*supi, profile.PcfInfo.SupiRanges)
		logger.NrfcacheLog.Infof("pcf match found = %v", matchFound)
		return matchFound, nil
	}

	// No SUPI filter - match any profile
	logger.NrfcacheLog.Infof("pcf match found = true (no SUPI filter)")
	return true, nil
}

func MatchUdmProfile(profile *models.NFProfileDiscovery, opts Nnrf_NFDiscovery.ApiSearchNFInstancesRequest) (bool, error) {
	supi := opts.GetSupi()
	if supi != nil {
		if profile.UdmInfo == nil || len(profile.UdmInfo.GetSupiRanges()) == 0 {
			logger.NrfcacheLog.Infof("udm match found = false (no SUPI ranges)")
			return false, nil
		}

		matchFound := matchSupiRange(*supi, profile.UdmInfo.GetSupiRanges())
		logger.NrfcacheLog.Infof("udm match found = %v", matchFound)
		return matchFound, nil
	}

	// No SUPI filter - match any profile
	logger.NrfcacheLog.Infof("udm match found = true (no SUPI filter)")
	return true, nil
}
