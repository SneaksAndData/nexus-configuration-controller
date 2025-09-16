/*
 * Copyright (c) 2025. ECCO Data & AI Open-Source Project Maintainers.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 *   Unless required by applicable law or agreed to in writing, software
 *   distributed under the License is distributed on an "AS IS" BASIS,
 *   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 *   See the License for the specific language governing permissions and
 *   limitations under the License.
 */

package models

import "time"

type AppConfig struct {
	Alias                      string        `mapstructure:"alias"`
	ControllerConfigPath       string        `mapstructure:"controller-config-path"`
	ShardConfigPath            string        `mapstructure:"shard-config-path"`
	ControllerNamespace        string        `mapstructure:"controller-namespace"`
	LogLevel                   string        `mapstructure:"log-level"`
	Workers                    int           `mapstructure:"workers"`
	FailureRateBaseDelay       time.Duration `mapstructure:"failure-rate-base-delay"`
	FailureRateMaxDelay        time.Duration `mapstructure:"failure-rate-max-delay"`
	RateLimitElementsPerSecond int           `mapstructure:"rate-limit-elements-per-second"`
	RateLimitElementsBurst     int           `mapstructure:"rate-limit-elements-burst"`
}
