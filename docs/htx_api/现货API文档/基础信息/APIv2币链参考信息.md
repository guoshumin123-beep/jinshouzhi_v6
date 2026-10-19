# /v2/reference/currencies (APIv2币链参考信息)

> 文档来源：https://www.htx.com/zh-cn/opend/newApiPages/?id=7ec478f0-7773-11ed-9966-0242ac110003

## 接口信息
| 项目 | 内容 |
|---|---|
| 请求类型 | GET |
| 是否验签 | 否 |
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
| currency | string | 否 | 币种<br>btc, ltc, bch, eth, etc ...(取值参考GET /v1/common/currencys) |
| authorizedUser | boolean | 否 | 已认证用户<br>true or false (如不填，缺省为true) |

## 响应参数
| 参数 | 数据类型 | 是否必填 | 描述 |
|---|---|---|---|
| code | int | 是 | 状态码 |
| message | string | 否 | 错误描述（如有） |
| <data> | object | 是 |  |
| currency | string | 是 | 币种 |
| <chains> | object | 是 |  |
| chain | string | 是 | 链名称 |
| displayName | string | 是 | 链显示名称 |
| baseChain | string | 否 | 底层链名称 |
| baseChainProtocol | string | 否 | 底层链协议 |
| contractAddress | string | 否 | 合约地址 |
| isDynamic | boolean | 否 | 是否动态手续费（仅对固定类型有效，withdrawFeeType=fixed）<br>true,false |
| numOfConfirmations | int | 是 | 安全上账所需确认次数（达到确认次数后允许提币） |
| numOfFastConfirmations | int | 是 | 快速上账所需确认次数（达到确认次数后允许交易但不允许提币） |
| minDepositAmt | string | 是 | 单次最小充币金额 |
| depositStatus | string | 是 | 充币状态<br>allowed,prohibited |
| minWithdrawAmt | string | 是 | 单次最小提币金额 |
| maxWithdrawAmt | string | 是 | 单次最大提币金额 |
| withdrawQuotaPerDay | string | 是 | 当日提币额度（新加坡时区） |
| withdrawQuotaPerYear | string | 是 | 当年提币额度 |
| withdrawQuotaTotal | string | 是 | 总提币额度 |
| withdrawPrecision | int | 是 | 提币精度 |
| withdrawFeeType | string | 是 | 提币手续费类型（特定币种在特定链上的提币手续费类型唯一）<br>fixed,circulated,ratio |
| transactFeeWithdraw | string | 否 | 单次提币手续费（仅对固定类型有效，withdrawFeeType=fixed） |
| minTransactFeeWithdraw | string | 否 | 最小单次提币手续费（仅对区间类型和有下限的比例类型有效，withdrawFeeType=circulated or ratio） |
| maxTransactFeeWithdraw | string | 否 | 最大单次提币手续费（仅对区间类型和有上限的比例类型有效，withdrawFeeType=circulated or ratio） |
| transactFeeRateWithdraw | string | 否 | 单次提币手续费率（仅对比例类型有效，withdrawFeeType=ratio） |
| withdrawStatus | string | 是 | 提币状态<br>allowed,prohibited |
| externalTransferEnabled | Boolean | 否 | false:不支持外部转账（当前仅支持poloniex），true：支持外部转账 |
| </data> |  | 否 |  |
| instStatus | string | 是 | 币种状态<br>normal,delisted |
| </chains> |  | 否 |  |

## 请求示例
```http
curl"https://api.huobi.pro/v2/reference/currencies?currency=usdt"
```

## 响应示例
```json
{
"code":200,
"data":[
{
"chains":[
{
"chain":"trc20usdt",
"displayName":"",
"baseChain":"TRX",
"baseChainProtocol":"TRC20",
"isDynamic":false,
"depositStatus":"allowed",
"maxTransactFeeWithdraw":"1.00000000",
"maxWithdrawAmt":"280000.00000000",
"minDepositAmt":"100",
"minTransactFeeWithdraw":"0.10000000",
"minWithdrawAmt":"0.01",
"numOfConfirmations":999,
"numOfFastConfirmations":999,
"withdrawFeeType":"circulated",
"withdrawPrecision":5,
"withdrawQuotaPerDay":"280000.00000000",
"withdrawQuotaPerYear":"2800000.00000000",
"withdrawQuotaTotal":"2800000.00000000",
"withdrawStatus":"allowed"
},
{
"chain":"usdt",
"displayName":"",
"baseChain":"BTC",
"baseChainProtocol":"OMNI",
"isDynamic":false,
"depositStatus":"allowed",
"maxWithdrawAmt":"19000.00000000",
"minDepositAmt":"0.0001",
"minWithdrawAmt":"2",
"numOfConfirmations":30,
"numOfFastConfirmations":15,
"transactFeeRateWithdraw":"0.00100000",
"withdrawFeeType":"ratio",
"withdrawPrecision":7,
"withdrawQuotaPerDay":"90000.00000000",
"withdrawQuotaPerYear":"111000.00000000",
"withdrawQuotaTotal":"1110000.00000000",
"withdrawStatus":"allowed"
},
{
"chain":"usdterc20",
"displayName":"",
"baseChain":"ETH",
"baseChainProtocol":"ERC20",
"isDynamic":false,
"depositStatus":"allowed",
"maxWithdrawAmt":"18000.00000000",
"minDepositAmt":"100",
"minWithdrawAmt":"1",
"numOfConfirmations":999,
"numOfFastConfirmations":999,
"transactFeeWithdraw":"0.10000000",
"withdrawFeeType":"fixed",
"withdrawPrecision":6,
"withdrawQuotaPerDay":"180000.00000000",
"withdrawQuotaPerYear":"200000.00000000",
"withdrawQuotaTotal":"300000.00000000",
"withdrawStatus":"allowed"
}
],
"currency":"usdt",
"instStatus":"normal"
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
