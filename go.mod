module github.com/snissn/compress

go 1.23

retract (
	// https://github.com/klauspost/compress/issues/1114
	v1.18.1

	// https://github.com/klauspost/compress/pull/503
	v1.14.3
	v1.14.2
	v1.14.1
)

require (
	github.com/golang/snappy v0.0.0-20180518054509-2e65f85255db // indirect
	github.com/syndtr/goleveldb v1.0.0 // indirect
)
