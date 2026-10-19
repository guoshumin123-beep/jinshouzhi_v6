# /v1/query/withdraw/client-order-id (通过clientOrderId查询提币订单)

> 文档来源：https://www.htx.com/zh-cn/opend/newApiPages/?id=7ec4516c-7773-11ed-9966-0242ac110003

## 接口信息
| 项目 | 内容 |
|---|---|
| 请求类型 | GET |
| 是否验签 | 是 |
| 接口权限 | 读取 |
| 接口描述 | - |

## 请求地址
| 环境 | 地址 |
|---|---|
| 线上环境 | `https://api.huobi.pro` |
| 线上环境（aws客户首选） | `https://api-aws.huobi.pro` |

## 请求参数
| 参数 | 数据类型 | 是否必填 | 描述 |
|---|---|---|---|
| clientOrderId | string | 是 | 客户端提币订单ID（最长32位的字符串） |

**请求参数说明**：<br/>用于查询api提币订单（api提币订单下单时使用了client-order-id，返回对应提币订单数据对象，如果api提币订单下单时没有传入client-order-id，返回空）

## 响应参数
| 参数 | 数据类型 | 是否必填 | 描述 |
|---|---|---|---|
| status > | stirng | 是 | 请求返回结果 |
| <data\ > | long | 否 |  |
| address | string | 是 | 地址 |
| client-order-id | string | 是 | 客户端提币订单ID |
| address-tag | string | 是 | 地址tag |
| amount | decimal | 是 | 数量 |
| chain | string | 否 | 区块链 |
| created-at | long | 是 | 创建时间 |
| currency | string | 是 | 币种 |
| error-code | string | 是 | 错误码 |
| error-msg | string | 是 | 错误信息 |
| fee | decimal | 是 | 手续费 |
| id | long | 是 | ID |
| state | string | 是 | 状态 |
| tx-hash | string | 是 | 交易哈希 |
| type | string | 是 | 类型 |
| updated-at | long | 是 | 更新时间 |
| </data> | long | 否 |  |

## 请求示例
```http
curl"https://api.huobi.pro/v1/query/withdraw/client-order-id?clientOrderId=xxxxx"
```

## 响应示例
```json
{
"status":"ok",
"data":{
"id":101123262,
"client-order-id":"1113",
"type":"withdraw",
"sub-type":"FAST",
"currency":"usdt",
"chain":"usdt",
"tx-hash":"",
"amount":1.200000000000000000,
"from-addr-tag":"",
"address":"1PL24EbWrNNrnMKw1cxAHPsebUz7DdhWTx",
"address-tag":"",
"fee":0E-18,
"state":"confirmed",
"created-at":1637758163686,
"updated-at":1637758251559
}
}
```

## SDK
- [JAVA SDK](https://github.com/HuobiRDCenter/huobi_Java)
- [PYTHON SDK](https://github.com/HuobiRDCenter/huobi_Python)
- [GO SDK](https://github.com/HuobiRDCenter/huobi_Golang)
- [C# SDK](https://github.com/HuobiRDCenter/huobi_CSharp)
- [C++ SDK](https://github.com/HuobiRDCenter/huobi_Cpp)
