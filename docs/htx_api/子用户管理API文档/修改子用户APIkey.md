# /v2/sub-user/api-key-modification (修改子用户APIkey)

> 文档来源：https://www.htx.com/zh-cn/opend/newApiPages/?id=7ec4840f-7773-11ed-9966-0242ac110003

## 接口信息
| 项目 | 内容 |
|---|---|
| 请求类型 | POST |
| 是否验签 | 是 |
| 接口权限 | 交易 |
| 接口描述 | 此接口用于母用户修改子用户的APIkey |

## 请求地址
| 环境 | 地址 |
|---|---|
| 线上环境 | `https://api.huobi.pro` |
| 线上环境（aws客户首选） | `https://api-aws.huobi.pro` |

## 请求参数
| 参数 | 数据类型 | 是否必填 | 描述 |
|---|---|---|---|
| subUid | long | 是 | 子用户的uid |
| accessKey | string | 是 | 子用户API key的access key |
| note | string | 否 | API key备注<br>最多255位字符 |
| permission | string | 否 | API key权限<br>取值范围：readOnly、trade，其中readOnly必传，trade选传，两个间用半角逗号分隔。 |
| ipAddresses | string | 否 | API key绑定的IP地址<br>IPv4/IPv6主机地址或IPv4网络地址，最多绑定20个，多个IP地址用半角逗号分隔，如：192.168.1.1,202.106.96.0/24。如果不绑定IP仅有只读权限的apikey永久有效；有交易或提现任意一权限的apikey，90天内不活跃将失效。 |

## 响应参数
| 参数 | 数据类型 | 是否必填 | 描述 |
|---|---|---|---|
| code | int | 是 | 状态码 |
| message | string | 否 | 错误描述（如有） |
| <data> | object | 是 |  |
| note | string | 否 | API key备注 |
| permission | string | 否 | API key权限 |
| ipAddresses | string | 否 | API key绑定的IP地址 |
| </data> |  | 否 |  |

## 请求示例
```http
{
"subUid":178211,
"accessKey":"178211",
"note":"178211",
"permission":"178211",
"ipAddresses":"178211"
}
```

## 响应示例
```json
{
"code":200,
"data":{
"note":"tom",
"permission":"trade,readOnly",
"ipAddresses":"192.168.1.1"
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
