// SPDX-License-Identifier: Apache-2.0

#[tokio::test]
async fn test_async_login() {
    let v = async { 1 }.await;
    assert_eq!(v, 1);
}
