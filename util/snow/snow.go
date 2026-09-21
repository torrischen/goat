package snow

import "github.com/bwmarrin/snowflake"

var snowflakeGenerator *snowflake.Node

func init() {
	_snowflakeGenerator, err := snowflake.NewNode(1)
	if err != nil {
		panic(err)
	}

	snowflakeGenerator = _snowflakeGenerator
}

func GenerateSnowflakeID() int64 {
	return snowflakeGenerator.Generate().Int64()
}

func GenerateSnowflakeIDString() string {
	return snowflakeGenerator.Generate().String()
}
