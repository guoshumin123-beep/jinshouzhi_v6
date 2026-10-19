# market.$symbol.fullDepth.$type (市场深度行情数据(含rpi))

> 文档来源：https://www.htx.com/zh-cn/opend/newApiPages/?id=8cb89359-77b5-11ed-9966-19feed47232

## 接口信息
| 项目 | 内容 |
|---|---|
| 请求类型 | - |
| 是否验签 | 否 |
| 接口权限 | 读取 |
| 接口描述 | 接口描述: 此主题发送最新市场深度快照。快照频率为每秒1次。数据包含了rpi订单 |

## 请求地址
| 环境 | 地址 |
|---|---|
| 线上环境 | `wss://api.huobi.pro/ws` |
| 线上环境（aws客户首选） | `wss://api-aws.huobi.pro/ws` |

## 请求参数
| 参数 | 数据类型 | 是否必填 | 描述 |
|---|---|---|---|
| symbol | Array | 是 | 交易代码<br>btcusdt, ethbtc...（取值参考GET /v1/common/symbols） |
| type | String | 是 | 合并深度类型<br>step0：无聚合，返回150档数据<br>step1：聚合度为报价精度*10<br>step2：聚合度为报价精度*100<br>step3：聚合度为报价精度*1000 <br>step4：聚合度为报价精度*5000<br>step5：聚合度为报价精度*100000 |
| cid | string | 否 | 表示本次请求id |

## 请求示例
```json
{
  "sub": ["market.btcusdt.fullDepth.step0","market.ethusdt.fullDepth.step0","market.trxusdt.fullDepth.step0"],
  "id": "id1"
}
```

## 响应示例
```json
{
"id":"id1",
"status":"ok",
"subbed":"market.btcusdt.fullDepth.step0",
"ts":1489474081631
}
```

## 推送数据
| 参数 | 数据类型 | 是否必填 | 描述 |
|---|---|---|---|
| ch | string | 是 | 数据所属的 channel，格式：market.$symbol.fullDepth.$type |
| ts | long | 是 | 系统响应时间 |
| <tick> | object | 否 |  |
| bids | object | 否 | 当前的所有买单 [price, size] |
| asks | object | 否 | 当前的所有卖单 [price, size] |
| version | integer | 否 | 内部字段 |
| ts | integer | 否 | 新加坡时间的时间戳，单位毫秒 |
| </tick> |  | 否 |  |

## 推送示例
```json
{
"ch":"market.btcusdt.fullDepth.step0",
"ts":1630983549503,
"tick":{
"bids":[
[
52690.69,
0.36281
],
[
52690.68,
0.2
]
],
"asks":[
[
52690.7,
0.372591
],
[
52691.26,
0.13
]
],
"version":136998124622,
"ts":1630983549500
}
}
```

## 取消订阅示例
```json
{
    "unsub": "market.btcusdt.fullDepth.step0",
    "id": "id1"
}
```

## SDK
- [JAVA SDK](https://github.com/HuobiRDCenter/huobi_Java)
- [PYTHON SDK](https://github.com/HuobiRDCenter/huobi_Python)
- [GO SDK](https://github.com/HuobiRDCenter/huobi_Golang)
- [C# SDK](https://github.com/HuobiRDCenter/huobi_CSharp)
- [C++ SDK](https://github.com/HuobiRDCenter/huobi_Cpp)
