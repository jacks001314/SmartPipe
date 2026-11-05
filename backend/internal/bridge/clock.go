package bridge

import "time"

// nowFunc 便于测试注入时间。
var nowFunc = time.Now
