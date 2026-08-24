package runtime

import "xirang/backend/internal/backupasset"

type foundationTransitionConfigs struct {
	Prior       backupasset.FoundationTransitionConfig
	Prospective backupasset.FoundationTransitionConfig
}

func foundationTransitionConfigsFromValues(priorValues, prospectiveValues map[string]string) (foundationTransitionConfigs, error) {
	prior, err := backupasset.FoundationTransitionConfigFromValues(priorValues)
	if err != nil {
		return foundationTransitionConfigs{}, err
	}
	prospective, err := backupasset.FoundationTransitionConfigFromValues(prospectiveValues)
	if err != nil {
		return foundationTransitionConfigs{}, err
	}
	return foundationTransitionConfigs{Prior: prior, Prospective: prospective}, nil
}
