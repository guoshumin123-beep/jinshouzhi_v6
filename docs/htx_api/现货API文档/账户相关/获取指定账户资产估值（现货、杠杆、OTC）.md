# /v2/account/asset-valuation (获取指定账户资产估值（现货、杠杆、OTC）)

> 文档来源：https://www.htx.com/zh-cn/opend/newApiPages/?id=7ec45e49-7773-11ed-9966-0242ac110003

## 接口信息
| 项目 | 内容 |
|---|---|
| 请求类型 | GET |
| 是否验签 | 是 |
| 接口权限 | 读取 |
| 限频 | 100次/2s |
| 接口描述 | 按照BTC或法币计价单位，获取指定账户的总资产估值。 |

## 请求地址
| 环境 | 地址 |
|---|---|
| 线上环境 | `https://api.huobi.pro` |
| 线上环境（aws客户首选） | `https://api-aws.huobi.pro` |

## 请求参数
| 参数 | 数据类型 | 是否必填 | 描述 |
|---|---|---|---|
| accountType | string | 是 | 账户类型<br>spot：现货账户， margin：逐仓杠杆账户，otc：OTC 账户，super-margin：全仓杠杆账户 |
| valuationCurrency | string | 否 | 资产估值法币，即资产按哪个法币为单位进行估值。<br>可选法币有：BTC、CNY、USD、JPY、KRW、GBP、TRY、EUR、RUB、VND、HKD、TWD、MYR、SGD、AED、SAR （大小写敏感） |
| subUid | long | 否 | 子用户的 UID，若不填，则返回API key所属用户的账户资产估值 |

## 响应参数
| 参数 | 数据类型 | 是否必填 | 描述 |
|---|---|---|---|
| code | string | 是 | 状态码 |
| ok | string | 是 | 请求结果 |
| <data> | object | 是 |  |
| balance | string | 是 | 按照某一个法币为单位的总资产估值 |
| timestamp | long | 是 | 数据返回时间，为unix time in millisecond |
| </data> |  | 否 |  |

## 请求示例
```http
curl"https://api.huobi.pro/v2/account/asset-valuation?accountType=spot&valuationCurrency=BTC&subUid=xxxx"
```

## 响应示例
```json
{
"code":200,
"data":{
"balance":"34.75",
"timestamp":1594901254363
},
"ok":true
}
```

## SDK
- [JAVA SDK](https://github.com/HuobiRDCenter/huobi_Java)
- [PYTHON SDK](https://github.com/HuobiRDCenter/huobi_Python)
- [GO SDK](https://github.com/HuobiRDCenter/huobi_Golang)
- [C# SDK](https://github.com/HuobiRDCenter/huobi_CSharp)
- [C++ SDK](https://github.com/HuobiRDCenter/huobi_Cpp)
