# /v2/algo-orders/opening (查询未触发OPEN策略委托)

> 文档来源：https://www.htx.com/zh-cn/opend/newApiPages/?id=7ec46ee3-7773-11ed-9966-0242ac110003

## 接口信息
| 项目 | 内容 |
|---|---|
| 请求类型 | GET |
| 是否验签 | 是 |
| 接口权限 | 读取 |
| 限频 | 20次/2s |
| 接口描述 | 以orderOrigTime检索<br>未触发OPEN订单，指的1已成功下单，但尚未触发，订单状态orderStatus为created的订单<br>未触发OPEN订单，可通过此节点查询，但不可通过现货/杠杆交易相关接口查询 |

## 请求地址
| 环境 | 地址 |
|---|---|
| 线上环境 | `https://api.huobi.pro` |
| 线上环境（aws客户首选） | `https://api-aws.huobi.pro` |

## 请求参数
| 参数 | 数据类型 | 是否必填 | 描述 |
|---|---|---|---|
| accountId | integer | 否 | 账户编号 |
| symbol | string | 否 | 交易代码 |
| orderSide | string | 否 | 订单方向<br>buy,sell |
| orderType | string | 否 | 订单类型<br>limit,market |
| sort | string | 否 | 检索方向<br>asc - 由远及近, desc - 由近及远 |
| limit | integer | 否 | 单页最大返回条目数量<br>[1-500] |
| fromId | long | 否 | 起始编号（仅在下页查询时有效） |

## 响应参数
| 参数 | 数据类型 | 是否必填 | 描述 |
|---|---|---|---|
| code | integer | 是 | 状态码 |
| message | string | 否 | 错误描述（如有） |
| <data> | object | 是 | 按用户请求参数sort指定顺序排列 |
| accountId | integer | 是 | 账户编号 |
| source | string | 是 | 订单来源（api,web,ios,android,mac,windows,sys） |
| clientOrderId | string | 是 | 用户自编订单号 |
| symbol | string | 是 | 交易代码 |
| orderPrice | string | 是 | 订单价格（对市价单无效） |
| orderSize | string | 否 | 订单数量（对市价买单无效） |
| orderValue | string | 否 | 订单金额（仅对市价买单有效） |
| orderSide | string | 是 | 订单方向 |
| timeInForce | string | 是 | 订单有效期 |
| orderType | string | 是 | 订单类型 |
| stopPrice | string | 是 | 触发价 |
| trailingRate | string | 否 | 回调幅度（仅对追踪委托有效） |
| orderOrigTime | long | 是 | 订单创建时间 |
| lastActTime | long | 是 | 订单最近更新时间 |
| orderStatus } | string | 是 | 订单状态（created） |
| </data> |  | 否 |  |
| nextId | long | 否 | 下页起始编号（仅在查询结果需要分页返回时传此字段） |

## 请求示例
```http
curl"https://api.huobi.pro/v2/algo-orders/opening?accountId=xxx&symbol=xxxx&orderSide=xxx&orderType=xxx"
```

## 响应示例
```json
{
"code":200,
"data":[
{
"lastActTime":1630657250326,
"orderOrigTime":1630657250238,
"symbol":"adausdt",
"source":"api",
"clientOrderId":"123",
"orderSide":"buy",
"orderType":"limit",
"orderPrice":"0.1",
"orderSize":"100",
"accountId":13496526,
"timeInForce":"gtc",
"stopPrice":"0.1",
"orderStatus":"created"
},
{
"lastActTime":1630657243576,
"orderOrigTime":1630657243534,
"symbol":"adausdt",
"source":"api",
"clientOrderId":"12",
"orderSide":"buy",
"orderType":"limit",
"orderPrice":"0.1",
"orderSize":"100",
"accountId":13496526,
"timeInForce":"gtc",
"stopPrice":"0.1",
"orderStatus":"created"
}
]
}
```

## SDK
- [JAVA SDK](https://github.com/HuobiRDCenter/huobi_Java)
- [PYTHON SDK](https://github.com/HuobiRDCenter/huobi_Python)
- [GO SDK](https://github.com/HuobiRDCenter/huobi_Golang)
- [C# SDK](https://github.com/HuobiRDCenter/huobi_CSharp)
- [C++ SDK](https://github.com/HuobiRDCenter/huobi_Cpp)
