package handlers

import "xirang/backend/internal/util"

func readBoolEnv(key string, defaultValue bool) (bool, error) {
	return util.ReadBoolEnv(key, defaultValue)
}

func expandHomePath(path string) (string, error) {
	return util.ExpandHomePath(path)
}
