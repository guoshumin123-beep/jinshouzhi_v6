# /v2/account/transfer (【通用】现货-合约账户和OTC账户间进行资金的划转)

> 文档来源：https://www.htx.com/zh-cn/opend/newApiPages/?id=10000095-77b7-11ed-9966-0242ac110003

## 接口信息
| 项目 | 内容 |
|---|---|
| 请求类型 | POST |
| 是否验签 | 是 |
| 接口权限 | 交易 |
| 接口描述 | 此接口用户币币现货账户与合约账户、OTC账户之间的资产划转。 |

## 请求地址
| 环境 | 地址 |
|---|---|
| 线上环境 | `https://api.huobi.pro` |
| 线上环境（aws客户首选） | `https://api-aws.huobi.pro` |

## 请求参数
| 参数 | 数据类型 | 是否必填 | 描述 |
|---|---|---|---|
| from | string | 是 | 来源业务线账户，取值：spot(币币)、linear-swap(U本位合约)、otc(OTC)、futures(反向交割)、swap(反向永续)<br>e.g. spot、linear-swap、otc、futures、swap |
| to | string | 是 | 来源业务线账户，取值：spot(币币)、linear-swap(U本位合约)、otc(OTC)、futures(反向交割)、swap(反向永续)<br>e.g. linear-swap、spot、otc、futures、swap |
| currency | string | 是 | 币种,支持大小写<br>e.g. usdt |
| amount | decimal | 是 | 划转金额 |
| margin-account | string | 是 | 保证金账户<br>e.g. btc-usdt、eth-usdt、USDT |

## 响应参数
| 参数 | 数据类型 | 是否必填 | 描述 |
|---|---|---|---|
| success | string | 是 | 状态<br>true/false |
| data | long | 是 | 生成的划转订单id |
| code | long | 是 | 响应码 |
| message | string | 是 | 响应信息 |

## 请求示例
```http
{  "from": "spot",  "to": "linear-swap",  "currency": "usdt",  "amount": 100,  "margin-account": "USDT"}
```

## 响应示例
```json
{    "code": 200,    "data": 176104252,    "message": "Succeed",    "success": true}
```

## SDK
- [JAVA SDK](https://github.com/HuobiRDCenter/huobi_Java)
- [PYTHON SDK](https://github.com/HuobiRDCenter/huobi_Python)
- [GO SDK](https://github.com/HuobiRDCenter/huobi_Golang)
- [C# SDK](https://github.com/HuobiRDCenter/huobi_CSharp)
- [C++ SDK](https://github.com/HuobiRDCenter/huobi_Cpp)
