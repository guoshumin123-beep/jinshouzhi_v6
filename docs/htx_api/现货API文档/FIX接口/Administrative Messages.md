# Administrative Messages

> 文档来源：https://www.htx.com/zh-cn/opend/newApiPages/?id=5486

Heartbeat ``

如果在心跳间隔（Logon`` 中的 `HeartBtInt`）的期间没有传出流量，则由服务器发送。

由客户端发送则用于指示会话正常。

| Tag | 名称 | 类型 | 是否必须 | 描述 |
|---|---|---|---|---|
| 112 | TestReqID | String | N | 当 Heartbeat 作为对 TestRequest 的响应发送时，必须镜像 TestRequest 中的值。 |

TestRequest ``

如果在心跳间隔（Logon`` 中的 `HeartBtInt`）的期间没有传入流量，则由服务器发送。

由客户端发送用于请求 Heartbeat`` 响应。

**注意:** 如果客户端未能在超时范围内发送带有正确 `TestReqID` 的 Heartbeat`` 来响应 TestRequest`` ，其连接将被断开。

| Tag | 名称 | 类型 | 必填 | 描述 |
|---|---|---|---|---|
| 112 | TestReqID | String | Y | 任意字符串，必须包含在 Heartbeat 响应中。 |

Reject ``

由服务器发送，用以响应无法处理的无效消息。包含句法错误，缺少必填字段，或引用未知交易对的客户端消息将被服务器拒绝。

| Tag | 名称 | 类型 | 必填 | 描述 |
|---|---|---|---|---|
| 45 | RefSeqNum | Int | N | 导致此 Reject 发送的被拒绝消息的 MsgSeqNum。 |
| 371 | RefTagID | Int | N | 如果存在，标识直接导致此 Reject 消息发送的字段。 |
| 372 | RefMsgType | String | N | 导致此 Reject 发送的被拒绝消息的 MsgType。 |
| 373 | SessionRejectReason | String | N | 拒绝的原因， |
| 58 | Text | String | N | 可读的错误信息。 |

Logon ``

由客户端发送，用以验证连接。 Logon`` 必须是客户端发送的第一条消息。

验证成功后由服务器发送，用以响应成功的登录。验证失败后会发送Logout<5> 消息。并断开连接

**注意:** Logon`` 在整个会话期间只能发送一次,MsgSeqNum 必须是1。

| Tag | 名称 | 类型 | 是否必须 | 描述 |
|---|---|---|---|---|
| 98 | EncryptMethod | Int | Y | 0 |
| 108 | HeartBtInt | Int | Y | 必须在 [5, 30] 范围内。Heartbeat 间隔（秒）。 |
| 95 | RawDataLength | Int | Y | 紧随此字段之后的 RawData字段的长度。 |
| 96 | RawData | String | Y | 签名。 使用密钥签署 payload。再使用 base64 对签名进行编码。 生成的文本字符串是 RawData 字段的值 |
| 141 | ResetSeqNumFlag | Boolean | Y | 必须为 Y。 |
| 553 | Username | String | Y | API key。必须是ed25519的api key。 |

验签payload （ | 是SOH 字符 ASCII值 (0x01)）：

     SenderCompId (49) |TargetCompId (56) | MsgSeqNum (34) | SendingTime (52) |

public static String sign25519(String SenderCompId,String TargetCompId,String MsgSeqNum,String SendingTime,String secretKey) {
    StringBuilder sb = new StringBuilder(1024);
    sb.append(SenderCompId).append('\u0001');
    sb.append(TargetCompId).append('\u0001');
    sb.append(MsgSeqNum).append('\u0001');
    sb.append(SendingTime).append('\u0001');

    byte[] dataBytes = sb.toString().getBytes(StandardCharsets.UTF_8);

    Ed25519PrivateKeyParameters privateKey = null;
    try {
        privateKey = (Ed25519PrivateKeyParameters) PrivateKeyFactory.createKey(Base64.getDecoder().decode(secretKey));
    } catch (IOException e) {
        throw new RuntimeException(e);
    }
    Ed25519Signer signer = new Ed25519Signer();
    signer.init(true, privateKey);
    signer.update(dataBytes, 0, dataBytes.length);
    byte[] signatureBytes = signer.generateSignature();
    String actualSign= Base64.getEncoder().encodeToString(signatureBytes);
    return actualSign;
}

Logout ``

由服务器发送，用来表示链接会关闭。

| Tag | 名称 | 类型 | 是否必须 | 描述 |
|---|---|---|---|---|
| 58 | Text | String | N |  |
