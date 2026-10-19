# /v2/user/api-key (母子用户APIkey信息查询)

> 文档来源：https://www.htx.com/zh-cn/opend/newApiPages/?id=7ec4910e-7773-11ed-9966-0242ac110003

## 接口信息
| 项目 | 内容 |
|---|---|
| 请求类型 | GET |
| 是否验签 | 是 |
| 接口权限 | 读取 |
| 接口描述 | 此接口用于母用户查询自身的APIkey信息，以及母用户查询子用户的APIkey信息 |

## 请求地址
| 环境 | 地址 |
|---|---|
| 线上环境 | `https://api.huobi.pro` |
| 线上环境（aws客户首选） | `https://api-aws.huobi.pro` |

## 请求参数
| 参数 | 数据类型 | 是否必填 | 描述 |
|---|---|---|---|
| uid | long | 是 | 母用户UID，子用户UID |
| accessKey | string | 否 | API key的access key，若缺省，则返回UID对应用户的所有API key |

## 响应参数
| 参数 | 数据类型 | 是否必填 | 描述 |
|---|---|---|---|
| code | int | 是 | 状态码 |
| message | string | 否 | 错误描述（如有） |
| <data> | object | 是 |  |
| accessKey | string | 是 | access key |
| note | string | 是 | API key备注 |
| permission | string | 是 | API key权限 |
| ipAddresses | string | 否 | API key绑定的IP地址 |
| validDays | int | 是 | API key剩余有效天数<br>若为-1，则表示永久有效 |
| status | string | 是 | API key当前状态<br>normal(正常)，expired(已过期) |
| createTime | long | 是 | API key创建时间 |
| updateTime | long | 是 | API key最近一次修改时间 |
| </data> |  | 否 |  |

## 请求示例
```http
curl"https://api.huobi.pro/v2/user/api-key?uid=xxx&accessKey=xxx"
```

## 响应示例
```json
{
"code":200,
"message":"success",
"data":[
{
"accessKey":"160bb889-b7XXXXbe-e0XXXXf5-ghxertfvbf",
"status":"normal",
"note":"host",
"permission":"trade,readOnly",
"ipAddresses":"192.168.0.1,192.168.1.1",
"validDays":-1,
"createTime":1615192704000,
"updateTime":1623030338000
},
{
"accessKey":"5000d371-edXXXXf5tf-40XXXX8b-ab8e5",
"status":"normal",
"note":"hosttwo",
"permission":"readOnly,trade,withdraw",
"ipAddresses":"",
"validDays":7,
"createTime":1623158078000,
"updateTime":1629875976000
}
],
"ok":true
}
```

## SDK
- [JAVA SDK](https://github.com/HuobiRDCenter/huobi_Java)
- [PYTHON SDK](https://github.com/HuobiRDCenter/huobi_Python)
- [GO SDK](https://github.com/HuobiRDCenter/huobi_Golang)
- [C# SDK](https://github.com/HuobiRDCenter/huobi_CSharp)
- [C++ SDK](https://github.com/HuobiRDCenter/huobi_Cpp)
