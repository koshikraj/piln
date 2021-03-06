/** @format */

import React from 'react'
import {createPortal} from 'react-dom'

export default class Portal extends React.Component {
  render() {
    let target = document.querySelector(this.props.to)

    if (target) {
      return createPortal(this.props.children, target)
    } else {
      return null
    }
  }
}
